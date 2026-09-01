package gate

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type testLogger struct{}

func (l testLogger) Debug(string, ...any)                   {}
func (l testLogger) Info(string, ...any)                    {}
func (l testLogger) Warn(string, ...any)                    {}
func (l testLogger) Error(string, ...any)                   {}
func (l testLogger) Fatal(string, ...any)                   {}
func (l testLogger) SetLevel(schemas.LogLevel)              {}
func (l testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// newTestProvider 构造指向 fake API server 的 provider；下载第二跳的 client
// 替换为直连 + 跳过证书校验的测试 client（生产路径由 SSRF-safe dialer 保护，
// 此处被有意替换，绝不能外泄到生产构造）。
func newTestProvider(t *testing.T, baseURL string) *GateProvider {
	t.Helper()
	provider, err := NewGateProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 5,
			MaxConnsPerHost:                4,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 1,
			BufferSize:  1,
		},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewGateProvider() error = %v", err)
	}
	provider.downloadClient = &fasthttp.Client{
		Dial:      func(addr string) (net.Conn, error) { return net.Dial("tcp", addr) },
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return provider
}

func testKey(secret string) schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar(secret)}
}

func testCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
}

func TestGateVideoGenerationRequestMapping(t *testing.T) {
	tests := []struct {
		name    string
		request *schemas.BifrostVideoGenerationRequest
		want    *GateVideoGenerationRequest
		wantErr string
	}{
		{
			name: "full fields",
			request: &schemas.BifrostVideoGenerationRequest{
				Model: "bytedance/seedance-2.0",
				Input: &schemas.VideoGenerationInput{Prompt: "a cat", InputReference: schemas.Ptr("https://cdn.example.com/a.png")},
				Params: &schemas.VideoGenerationParameters{
					Seconds: schemas.Ptr("6"),
					Size:    "1280x720",
					Audio:   schemas.Ptr(true),
					Seed:    schemas.Ptr(42),
					ExtraParams: map[string]any{
						"resolution":   "720p",
						"aspect_ratio": "16:9",
						"metadata":     map[string]string{"source": "playground"},
					},
				},
			},
			want: &GateVideoGenerationRequest{
				Model:         "bytedance/seedance-2.0",
				Prompt:        "a cat",
				Duration:      schemas.Ptr(6),
				Resolution:    "720p",
				AspectRatio:   "16:9",
				GenerateAudio: schemas.Ptr(true),
				Seed:          schemas.Ptr(42),
				Size:          "1280x720",
				InputReferences: []GateInputReference{{
					Type: "image",
					URL:  "https://cdn.example.com/a.png",
					Role: "first_frame",
				}},
				Metadata: map[string]string{"source": "playground"},
			},
		},
		{
			name: "minimal",
			request: &schemas.BifrostVideoGenerationRequest{
				Model: "m",
				Input: &schemas.VideoGenerationInput{Prompt: "p"},
			},
			want: &GateVideoGenerationRequest{Model: "m", Prompt: "p"},
		},
		{
			name: "missing prompt rejected",
			request: &schemas.BifrostVideoGenerationRequest{
				Model: "m",
				Input: &schemas.VideoGenerationInput{},
			},
			wantErr: "prompt is required",
		},
		{
			name: "invalid seconds rejected",
			request: &schemas.BifrostVideoGenerationRequest{
				Model:  "m",
				Input:  &schemas.VideoGenerationInput{Prompt: "p"},
				Params: &schemas.VideoGenerationParameters{Seconds: schemas.Ptr("six")},
			},
			wantErr: "invalid seconds value",
		},
		{
			name: "unknown extra param rejected",
			request: &schemas.BifrostVideoGenerationRequest{
				Model:  "m",
				Input:  &schemas.VideoGenerationInput{Prompt: "p"},
				Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{"webhook_url": "https://evil.example.com"}},
			},
			wantErr: "unsupported extra param",
		},
		{
			name: "non-string resolution rejected",
			request: &schemas.BifrostVideoGenerationRequest{
				Model:  "m",
				Input:  &schemas.VideoGenerationInput{Prompt: "p"},
				Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{"resolution": 720}},
			},
			wantErr: "resolution must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToGateVideoGenerationRequest(tt.request)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ToGateVideoGenerationRequest() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ToGateVideoGenerationRequest() error = %v", err)
			}
			gotJSON, _ := sonic.Marshal(got)
			wantJSON, _ := sonic.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("ToGateVideoGenerationRequest() = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestMapGateStatus(t *testing.T) {
	tests := []struct {
		status  string
		want    schemas.VideoStatus
		wantErr bool
	}{
		{"pending", schemas.VideoStatusQueued, false},
		{"in_progress", schemas.VideoStatusInProgress, false},
		{"completed", schemas.VideoStatusCompleted, false},
		{"failed", schemas.VideoStatusFailed, false},
		{"queued", "", true},
		{"", "", true},
		{"RUNNING", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got, err := mapGateStatus(tt.status)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("mapGateStatus(%q) unexpectedly succeeded", tt.status)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("mapGateStatus(%q) = %q, %v; want %q", tt.status, got, err, tt.want)
			}
		})
	}
}

// recordedRequest 捕获 fake server 收到的请求要素。
type recordedRequest struct {
	method         string
	path           string
	authorization  string
	idempotencyKey string
	body           []byte
}

func TestGateVideoGenerationSubmit(t *testing.T) {
	var recorded recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded.method = r.Method
		recorded.path = r.URL.Path
		recorded.authorization = r.Header.Get("Authorization")
		recorded.idempotencyKey = r.Header.Get("Idempotency-Key")
		recorded.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"video_abc123","status":"in_progress","model":"bytedance/seedance-2.0","status_url":"https://api.gate.ai/api/v1/videos/video_abc123","message":"已提交","current_balance":"100.0000000000","estimated_cost":"1.0800000000","pre_deduct_amount":"1.0800000000","balance_after_estimate":"98.9200000000","currency":"USDT","billing_notice":"..."}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := testCtx()
	// 幂等键经既有 ExtraHeaders 契约传递。
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"Idempotency-Key": {"llmgw-exec-42"}})

	resp, bifrostErr := provider.VideoGeneration(ctx, testKey("gate-secret"), &schemas.BifrostVideoGenerationRequest{
		Model:  "bytedance/seedance-2.0",
		Input:  &schemas.VideoGenerationInput{Prompt: "a cat"},
		Params: &schemas.VideoGenerationParameters{Seconds: schemas.Ptr("6"), ExtraParams: map[string]any{"resolution": "720p"}},
	})
	if bifrostErr != nil {
		t.Fatalf("VideoGeneration() error = %+v", bifrostErr.Error)
	}

	// 请求侧：路径、方法、鉴权、幂等头、请求体
	if recorded.method != http.MethodPost || recorded.path != "/api/v1/videos" {
		t.Fatalf("request = %s %s", recorded.method, recorded.path)
	}
	if recorded.authorization != "Bearer gate-secret" {
		t.Fatalf("Authorization = %q", recorded.authorization)
	}
	if recorded.idempotencyKey != "llmgw-exec-42" {
		t.Fatalf("Idempotency-Key = %q", recorded.idempotencyKey)
	}
	var body map[string]any
	if err := sonic.Unmarshal(recorded.body, &body); err != nil {
		t.Fatalf("request body not JSON: %v (%s)", err, recorded.body)
	}
	if body["model"] != "bytedance/seedance-2.0" || body["prompt"] != "a cat" || body["resolution"] != "720p" {
		t.Fatalf("body = %s", recorded.body)
	}
	if body["duration"] != float64(6) {
		t.Fatalf("duration = %v", body["duration"])
	}

	// 响应侧：opaque ID 带 :gate 后缀、状态映射；Submit 只有预估费用，
	// 无终态 settled 费用，Cost 必须为 nil。
	if resp.ID != "video_abc123:gate" {
		t.Fatalf("ID = %q", resp.ID)
	}
	if resp.Status != schemas.VideoStatusInProgress {
		t.Fatalf("Status = %q", resp.Status)
	}
	if resp.Cost != nil {
		t.Fatalf("submit response must not carry terminal cost: %+v", resp.Cost)
	}
}

// TestGateTerminalCost 锁定 settled 映射：只有 billing_status=settled 且
// billed_cost 为有限非负数才产生 Cost。
func TestGateTerminalCost(t *testing.T) {
	tests := []struct {
		name          string
		billingStatus string
		billedCost    string
		want          *float64
	}{
		{"settled positive", "settled", "1.0800000000", schemas.Ptr(1.08)},
		{"settled zero", "settled", "0.0000000000", schemas.Ptr(0.0)},
		{"pre_deducted", "pre_deducted", "1.0", nil},
		{"missing status", "", "1.0", nil},
		{"unknown status", "charged", "1.0", nil},
		{"settled missing cost", "settled", "", nil},
		{"settled invalid", "settled", "abc", nil},
		{"settled negative", "settled", "-0.1", nil},
		{"settled infinity", "settled", "Inf", nil},
		{"settled NaN", "settled", "NaN", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gateTerminalCost(tt.billingStatus, tt.billedCost)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("gateTerminalCost(%q, %q) = %+v, want nil", tt.billingStatus, tt.billedCost, got)
				}
				return
			}
			if got == nil || got.TotalCost != *tt.want {
				t.Fatalf("gateTerminalCost(%q, %q) = %+v, want %v", tt.billingStatus, tt.billedCost, got, *tt.want)
			}
		})
	}
}

func TestGateVideoGenerationSubmitWithoutIdempotencyKey(t *testing.T) {
	var gotIdempotency bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdempotency = r.Header.Get("Idempotency-Key") != ""
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"j1","status":"pending"}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	if _, bifrostErr := provider.VideoGeneration(testCtx(), testKey("s"), &schemas.BifrostVideoGenerationRequest{
		Model: "m", Input: &schemas.VideoGenerationInput{Prompt: "p"},
	}); bifrostErr != nil {
		t.Fatalf("VideoGeneration() error = %+v", bifrostErr.Error)
	}
	if gotIdempotency {
		t.Fatal("Idempotency-Key must be omitted when context carries none")
	}
}

func TestGateVideoGenerationSubmitRejected(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		body       string
		wantStatus int
		wantMsg    string
	}{
		{"bad request", http.StatusBadRequest, `{"code":400,"msg":"参数错误"}`, 400, "参数错误"},
		{"payment required", http.StatusPaymentRequired, `{"code":402,"msg":"余额不足"}`, 402, "余额不足"},
		{"server error keeps status", http.StatusInternalServerError, `{"code":500,"msg":"boom"}`, 500, "boom"},
		{"non-json error body", http.StatusBadGateway, `upstream down`, 502, "upstream down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.httpStatus)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			_, bifrostErr := provider.VideoGeneration(testCtx(), testKey("s"), &schemas.BifrostVideoGenerationRequest{
				Model: "m", Input: &schemas.VideoGenerationInput{Prompt: "p"},
			})
			if bifrostErr == nil {
				t.Fatal("VideoGeneration() expected error")
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tt.wantStatus {
				t.Fatalf("StatusCode = %v, want %d", bifrostErr.StatusCode, tt.wantStatus)
			}
			if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, tt.wantMsg) {
				t.Fatalf("Message = %+v, want %q", bifrostErr.Error, tt.wantMsg)
			}
		})
	}
}

func TestGateVideoGenerationEnvelopeCodeRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"code":429,"msg":"请求过于频繁"}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	_, bifrostErr := provider.VideoGeneration(testCtx(), testKey("s"), &schemas.BifrostVideoGenerationRequest{
		Model: "m", Input: &schemas.VideoGenerationInput{Prompt: "p"},
	})
	if bifrostErr == nil || bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "429") {
		t.Fatalf("expected envelope-code rejection, got %+v", bifrostErr)
	}
}

func TestGateVideoGenerationUnknownStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"j1","status":"teleporting"}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	_, bifrostErr := provider.VideoGeneration(testCtx(), testKey("s"), &schemas.BifrostVideoGenerationRequest{
		Model: "m", Input: &schemas.VideoGenerationInput{Prompt: "p"},
	})
	if bifrostErr == nil || !strings.Contains(fmt.Sprintf("%+v", bifrostErr), "unknown gate task status") {
		t.Fatalf("unknown status must be a protocol error, got %+v", bifrostErr)
	}
}

func TestGateVideoRetrieveMapping(t *testing.T) {
	var recordedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer gate-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"video_abc123","status":"completed","model":"bytedance/seedance-2.0","download_url":"https://api.gate.ai/api/v1/videos/video_abc123/content","duration":6,"resolution":"720p","aspect_ratio":"16:9","generate_audio":false,"estimated_cost":"1.0800000000","billed_cost":"1.0800000001","billing_status":"settled","currency":"USD","expires_at":"2026-06-26T05:00:00Z","created_at":"2026-05-27T05:00:00Z","completed_at":"2026-05-27T05:03:00Z"}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	resp, bifrostErr := provider.VideoRetrieve(testCtx(), testKey("gate-secret"), &schemas.BifrostVideoRetrieveRequest{
		Provider: schemas.Gate,
		ID:       "video_abc123:gate",
	})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}

	// opaque ID 的 :gate 后缀在出网前剥离
	if recordedPath != "/api/v1/videos/video_abc123" {
		t.Fatalf("path = %q", recordedPath)
	}
	if resp.ID != "video_abc123:gate" || resp.Status != schemas.VideoStatusCompleted {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Seconds == nil || *resp.Seconds != "6" {
		t.Fatalf("Seconds = %+v", resp.Seconds)
	}
	wantCreated := time.Date(2026, 5, 27, 5, 0, 0, 0, time.UTC).Unix()
	if resp.CreatedAt != wantCreated {
		t.Fatalf("CreatedAt = %d, want %d", resp.CreatedAt, wantCreated)
	}
	wantCompleted := time.Date(2026, 5, 27, 5, 3, 0, 0, time.UTC).Unix()
	if resp.CompletedAt == nil || *resp.CompletedAt != wantCompleted {
		t.Fatalf("CompletedAt = %+v", resp.CompletedAt)
	}
	wantExpires := time.Date(2026, 6, 26, 5, 0, 0, 0, time.UTC).Unix()
	if resp.ExpiresAt == nil || *resp.ExpiresAt != wantExpires {
		t.Fatalf("ExpiresAt = %+v", resp.ExpiresAt)
	}
	if len(resp.Videos) != 1 || resp.Videos[0].URL == nil ||
		*resp.Videos[0].URL != "https://api.gate.ai/api/v1/videos/video_abc123/content" {
		t.Fatalf("Videos = %+v", resp.Videos)
	}
	// settled 终态映射 Cost；原始十进制文本只进 RawResponse（本测试未开启）。
	if resp.Cost == nil {
		t.Fatal("settled terminal must carry Cost")
	}
	want, _ := strconv.ParseFloat("1.0800000001", 64)
	if resp.Cost.TotalCost != want {
		t.Fatalf("Cost = %+v, want %v", resp.Cost.TotalCost, want)
	}
}

func TestGateVideoRetrieveNonTerminalKeepsNoTerminalFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"j1","status":"in_progress","estimated_cost":"1.0000000000","billing_status":"pre_deducted","created_at":"2026-05-27T05:00:00Z"}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	resp, bifrostErr := provider.VideoRetrieve(testCtx(), testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}
	if resp.Status != schemas.VideoStatusInProgress {
		t.Fatalf("Status = %q", resp.Status)
	}
	if resp.CompletedAt != nil || resp.ExpiresAt != nil || len(resp.Videos) != 0 {
		t.Fatalf("non-terminal response must not carry terminal fields: %+v", resp)
	}
	// pre_deducted 不是终态结算证据：Cost 保持 nil。
	if resp.Cost != nil {
		t.Fatalf("pre_deducted must not map to Cost: %+v", resp.Cost)
	}
}

func TestGateVideoRetrieveFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,"msg":"内容审核未通过","data":{"job_id":"j1","status":"failed","billed_cost":"0.5000000000","billing_status":"settled","created_at":"2026-05-27T05:00:00Z","completed_at":"2026-05-27T05:01:00Z"}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	resp, bifrostErr := provider.VideoRetrieve(testCtx(), testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}
	if resp.Status != schemas.VideoStatusFailed {
		t.Fatalf("Status = %q", resp.Status)
	}
	if resp.Error == nil || resp.Error.Code != "failed" || resp.Error.Message != "内容审核未通过" {
		t.Fatalf("Error = %+v", resp.Error)
	}
	// 失败任务仍可能产生正费用：settled 实际收费映射 Cost。
	if resp.Cost == nil || resp.Cost.TotalCost != 0.5 {
		t.Fatalf("failed task settled cost = %+v", resp.Cost)
	}
}

// TestGateVideoRetrieveRawResponse 验证开启 sendBackRawResponse 时原始
// billed_cost 字符串原样保留在 RawResponse 中。
func TestGateVideoRetrieveRawResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"j1","status":"completed","billed_cost":"1.0800000001","billing_status":"settled","created_at":"2026-05-27T05:00:00Z","completed_at":"2026-05-27T05:03:00Z"}}`)
	}))
	defer server.Close()

	provider, err := NewGateProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 5,
			MaxConnsPerHost:                4,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
		SendBackRawResponse:      true,
	}, testLogger{})
	if err != nil {
		t.Fatal(err)
	}

	resp, bifrostErr := provider.VideoRetrieve(testCtx(), testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}
	raw, ok := resp.ExtraFields.RawResponse.(json.RawMessage)
	if !ok || !strings.Contains(string(raw), `"billed_cost":"1.0800000001"`) {
		t.Fatalf("raw response must preserve exact billed_cost string, got %T %+v", resp.ExtraFields.RawResponse, resp.ExtraFields.RawResponse)
	}
}

func TestGateVideoRetrieveUnknownStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"j1","status":"mysterious"}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	_, bifrostErr := provider.VideoRetrieve(testCtx(), testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr == nil || !strings.Contains(fmt.Sprintf("%+v", bifrostErr), "unknown gate task status") {
		t.Fatalf("unknown status must be a protocol error, got %+v", bifrostErr)
	}
}

func TestGateVideoRetrieveNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"code":404,"msg":"任务不存在"}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	_, bifrostErr := provider.VideoRetrieve(testCtx(), testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "ghost:gate"})
	if bifrostErr == nil {
		t.Fatal("VideoRetrieve() expected error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusNotFound {
		t.Fatalf("StatusCode = %v", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != ErrCodeVideoNotFound {
		t.Fatalf("Code = %+v", bifrostErr.Error)
	}
}

// TestGateVideoDownloadTwoHop 验证两跳下载：第一跳携带 Bearer 且不自动跟随
// 重定向；第二跳是全新请求，绝不携带 Authorization / Idempotency-Key；
// 响应体经 large-response context 契约完整流出。
func TestGateVideoDownloadTwoHop(t *testing.T) {
	payload := make([]byte, 2*1024*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	var cdnAuth, cdnIdempotency string
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnAuth = r.Header.Get("Authorization")
		cdnIdempotency = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(payload)
	}))
	defer cdn.Close()

	var apiAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/videos/video_abc123/content" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Location", cdn.URL+"/videos/video_abc123.mp4?expires=1780000000")
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()

	provider := newTestProvider(t, api.URL)
	ctx := testCtx()
	// 幂等键经 ExtraHeaders 契约传递；第二跳是全新请求，绝不携带它。
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"Idempotency-Key": {"llmgw-exec-42"}})
	resp, bifrostErr := provider.VideoDownload(ctx, testKey("gate-secret"), &schemas.BifrostVideoDownloadRequest{
		Provider: schemas.Gate,
		ID:       "video_abc123:gate",
	})
	if bifrostErr != nil {
		t.Fatalf("VideoDownload() error = %+v", bifrostErr.Error)
	}

	if apiAuth != "Bearer gate-secret" {
		t.Fatalf("hop-1 Authorization = %q", apiAuth)
	}
	if cdnAuth != "" || cdnIdempotency != "" {
		t.Fatalf("hop-2 leaked headers: Authorization=%q Idempotency-Key=%q", cdnAuth, cdnIdempotency)
	}
	// 元数据响应不携带字节；实际流走 large-response context 契约。
	if len(resp.Content) != 0 {
		t.Fatalf("Content must stay empty, got %d bytes", len(resp.Content))
	}
	reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(*providerUtils.LargeResponseReader)
	if !ok || reader == nil {
		t.Fatal("large response reader not registered in context")
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseContentType).(string); got != "video/mp4" {
		t.Fatalf("registered content type = %q", got)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("streamed %d bytes, want %d", len(got), len(payload))
	}
	for i := 0; i < len(payload); i += 4096 {
		if got[i] != payload[i] {
			t.Fatalf("payload mismatch at %d", i)
		}
	}
	if resp.VideoID != "video_abc123:gate" || resp.ContentType != "video/mp4" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestGateVideoDownloadStatusMapping(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		wantCode   string
	}{
		{"not found", http.StatusNotFound, ErrCodeVideoNotFound},
		{"not ready", http.StatusConflict, ErrCodeVideoNotReady},
		{"expired", http.StatusGone, ErrCodeVideoExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.httpStatus)
				fmt.Fprint(w, `{"code":`+fmt.Sprint(tt.httpStatus)+`,"msg":"x"}`)
			}))
			defer api.Close()

			provider := newTestProvider(t, api.URL)
			_, bifrostErr := provider.VideoDownload(testCtx(), testKey("s"), &schemas.BifrostVideoDownloadRequest{ID: "j1:gate"})
			if bifrostErr == nil {
				t.Fatal("VideoDownload() expected error")
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tt.httpStatus {
				t.Fatalf("StatusCode = %v, want %d", bifrostErr.StatusCode, tt.httpStatus)
			}
			if bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != tt.wantCode {
				t.Fatalf("Code = %+v, want %q", bifrostErr.Error, tt.wantCode)
			}
		})
	}
}

// TestGateVideoDownloadRejectsInsecureLocation 验证非 HTTPS Location 在第二跳
// 发出前被拒绝，CDN 不会被触达。
func TestGateVideoDownloadRejectsInsecureLocation(t *testing.T) {
	cdnHit := false
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnHit = true
	}))
	defer cdn.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", cdn.URL+"/video.mp4")
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()

	provider := newTestProvider(t, api.URL)
	_, bifrostErr := provider.VideoDownload(testCtx(), testKey("s"), &schemas.BifrostVideoDownloadRequest{ID: "j1:gate"})
	if bifrostErr == nil || !strings.Contains(bifrostErr.Error.Message, "not https") {
		t.Fatalf("expected https rejection, got %+v", bifrostErr)
	}
	if cdnHit {
		t.Fatal("CDN must not be contacted for insecure Location")
	}
}

func TestValidateDownloadLocation(t *testing.T) {
	tests := []struct {
		name     string
		location string
		wantErr  string
	}{
		{"https ok", "https://cdn.example.com/v.mp4?expires=1", ""},
		{"http rejected", "http://cdn.example.com/v.mp4", "not https"},
		{"relative rejected", "/v.mp4", "absolute"},
		{"userinfo rejected", "https://user:pass@cdn.example.com/v.mp4", "userinfo"},
		{"empty host rejected", "https:///v.mp4", "absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDownloadLocation(tt.location)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateDownloadLocation() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateDownloadLocation() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestGateProviderFailClosed 验证非视频能力全部在发网前拒绝。
func TestGateProviderFailClosed(t *testing.T) {
	provider := newTestProvider(t, "http://127.0.0.1:1")
	ctx := testCtx()
	key := testKey("s")

	if _, err := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{}); err == nil {
		t.Fatal("ChatCompletion must be unsupported")
	}
	if _, err := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{}); err == nil {
		t.Fatal("ListModels must be unsupported")
	}
	if _, err := provider.VideoDelete(ctx, key, &schemas.BifrostVideoDeleteRequest{}); err == nil {
		t.Fatal("VideoDelete must be unsupported")
	}
	if _, err := provider.VideoList(ctx, key, &schemas.BifrostVideoListRequest{}); err == nil {
		t.Fatal("VideoList must be unsupported")
	}
	if _, err := provider.VideoRemix(ctx, key, &schemas.BifrostVideoRemixRequest{}); err == nil {
		t.Fatal("VideoRemix must be unsupported")
	}
	if _, err := provider.Embedding(ctx, key, &schemas.BifrostEmbeddingRequest{}); err == nil {
		t.Fatal("Embedding must be unsupported")
	}
}

// TestDirectKeyViaSetValue 验证嵌入式 owner 在调用前用既有 SetValue 契约
// 固定未注册/disabled 历史 key（新建 context 默认不写锁）。
func TestDirectKeyViaSetValue(t *testing.T) {
	ctx := testCtx()
	key := testKey("disabled-pc-secret")
	ctx.SetValue(schemas.BifrostContextKeyDirectKey, key)
	got, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key)
	if !ok || got.Value.GetValue() != "disabled-pc-secret" {
		t.Fatalf("DirectKey = %+v, ok=%v", got, ok)
	}
}
