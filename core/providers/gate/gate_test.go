package gate

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
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
	return newTestProviderWithTimeout(t, baseURL, 5)
}

func newTestProviderWithTimeout(t *testing.T, baseURL string, timeoutSeconds int) *GateProvider {
	t.Helper()
	provider, err := NewGateProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: timeoutSeconds,
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
	// 仅替换测试环境的拨号与证书校验，保留生产构造写入 client 的全部
	// deadline/连接生命周期设置；否则测试会意外把待验证能力覆盖掉。
	testDialer := &net.Dialer{}
	provider.downloadDial = testDialer.DialContext
	provider.downloadClient.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	return provider
}

func testKey(secret string) schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar(secret)}
}

func testCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
}

// testPayload 生成确定性的伪随机字节（不可压压缩，适合 gzip 路径断言）。
func testPayload(n int) []byte {
	p := make([]byte, n)
	_, _ = rand.New(rand.NewSource(42)).Read(p)
	return p
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
			name: "negative prompt rejected",
			request: &schemas.BifrostVideoGenerationRequest{
				Model:  "m",
				Input:  &schemas.VideoGenerationInput{Prompt: "p"},
				Params: &schemas.VideoGenerationParameters{NegativePrompt: schemas.Ptr("bad weather")},
			},
			wantErr: "negative_prompt is not supported",
		},
		{
			name: "video uri rejected",
			request: &schemas.BifrostVideoGenerationRequest{
				Model:  "m",
				Input:  &schemas.VideoGenerationInput{Prompt: "p"},
				Params: &schemas.VideoGenerationParameters{VideoURI: schemas.Ptr("https://cdn.example.com/source.mp4")},
			},
			wantErr: "video_uri is not supported",
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
	providerSecret string
	contextSecret  string
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

	// 响应侧：opaque ID 带 :gate 后缀、状态映射。Submit currency 按任务
	// 身份原样保留，但不能产生终态 settled sidecar。
	if resp.ID != "video_abc123:gate" {
		t.Fatalf("ID = %q", resp.ID)
	}
	if resp.Status != schemas.VideoStatusInProgress {
		t.Fatalf("Status = %q", resp.Status)
	}
	if taskID, currency, ok := VideoSubmissionBillingFromContext(ctx); !ok || taskID != resp.ID || currency != "USDT" {
		t.Fatalf("submission billing = task=%q currency=%q ok=%v", taskID, currency, ok)
	}
	if taskID, cost, currency, ok := SettledVideoBillingFromContext(ctx); ok || taskID != "" || cost != "" || currency != "" {
		t.Fatalf("submit must not publish settled billing: task=%q cost=%q currency=%q ok=%v", taskID, cost, currency, ok)
	}
}

// TestGateVideoGenerationSubmitOnlyAccepts202 锁定官方合同：成功提交只接受
// HTTP 202；没有真实线上证据前，200 不能被静默扩成成功。
func TestGateVideoGenerationSubmitOnlyAccepts202(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"j1","status":"pending"}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := testCtx()
	setVideoSubmissionBilling(ctx, "stale:gate", "USD")
	_, bifrostErr := provider.VideoGeneration(ctx, testKey("s"), &schemas.BifrostVideoGenerationRequest{
		Model: "m", Input: &schemas.VideoGenerationInput{Prompt: "p"},
	})
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusOK {
		t.Fatalf("HTTP 200 must be rejected, got %+v", bifrostErr)
	}
	if taskID, currency, ok := VideoSubmissionBillingFromContext(ctx); ok || taskID != "" || currency != "" {
		t.Fatalf("rejected submit leaked stale billing: task=%q currency=%q ok=%v", taskID, currency, ok)
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
	ctx := testCtx()
	if _, bifrostErr := provider.VideoGeneration(ctx, testKey("s"), &schemas.BifrostVideoGenerationRequest{
		Model: "m", Input: &schemas.VideoGenerationInput{Prompt: "p"},
	}); bifrostErr != nil {
		t.Fatalf("VideoGeneration() error = %+v", bifrostErr.Error)
	}
	if gotIdempotency {
		t.Fatal("Idempotency-Key must be omitted when context carries none")
	}
	if taskID, currency, ok := VideoSubmissionBillingFromContext(ctx); ok || taskID != "" || currency != "" {
		t.Fatalf("missing upstream currency must remain absent: task=%q currency=%q ok=%v", taskID, currency, ok)
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
		{"unauthorized", http.StatusUnauthorized, `{"code":401,"msg":"鉴权失败"}`, 401, "鉴权失败"},
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
	var recordedPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedPaths = append(recordedPaths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer gate-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/content") {
			// 直链只经 302 Location 交出；指向不可路由的 CDN 主机，
			// 若实现错误地跟随重定向，本测试必然失败或超时。
			w.Header().Set("Location", "https://cdn.example.com/signed-direct.mp4")
			w.WriteHeader(http.StatusFound)
			return
		}
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"video_abc123","status":"completed","model":"bytedance/seedance-2.0","download_url":"https://api.gate.ai/api/v1/videos/video_abc123/content","duration":6,"resolution":"720p","aspect_ratio":"16:9","generate_audio":false,"estimated_cost":"1.0800000000","billed_cost":"1.0800000001","billing_status":"settled","currency":"USD","expires_at":"2026-06-26T05:00:00Z","created_at":"2026-05-27T05:00:00Z","completed_at":"2026-05-27T05:03:00Z"}}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := testCtx()
	resp, bifrostErr := provider.VideoRetrieve(ctx, testKey("gate-secret"), &schemas.BifrostVideoRetrieveRequest{
		Provider: schemas.Gate,
		ID:       "video_abc123:gate",
	})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}

	// opaque ID 的 :gate 后缀在出网前剥离；完成后追加 /content 第一跳取直链
	wantPaths := []string{"/api/v1/videos/video_abc123", "/api/v1/videos/video_abc123/content"}
	if fmt.Sprintf("%v", recordedPaths) != fmt.Sprintf("%v", wantPaths) {
		t.Fatalf("paths = %v, want %v", recordedPaths, wantPaths)
	}
	if resp.Size != "720p" {
		t.Fatalf("Size = %q, want resolution mapped 720p", resp.Size)
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
		*resp.Videos[0].URL != "https://cdn.example.com/signed-direct.mp4" {
		t.Fatalf("Videos = %+v", resp.Videos)
	}
	// 费用只写 Gate provider-local sidecar，Core 视频响应 schema 不扩张；
	// 原始十进制必须逐字保留，不能经 float64 往返。
	if taskID, cost, currency, ok := SettledVideoBillingFromContext(ctx); !ok || taskID != resp.ID || cost != "1.0800000001" || currency != "USD" {
		t.Fatalf("settled billing = task=%q cost=%q currency=%q ok=%v", taskID, cost, currency, ok)
	}
}

func gateRetrieveEnvelope(jobID, status, billingStatus, billedCost string, terminalFields bool) string {
	data := map[string]any{
		"job_id":         jobID,
		"status":         status,
		"billing_status": billingStatus,
		"billed_cost":    billedCost,
		"created_at":     "2026-05-27T05:00:00Z",
	}
	if terminalFields {
		data["completed_at"] = "2026-05-27T05:01:00Z"
		data["expires_at"] = "2026-06-27T05:01:00Z"
		data["download_url"] = "https://cdn.example.com/v.mp4"
	}
	body, _ := json.Marshal(map[string]any{"code": 200, "msg": "", "data": data})
	return string(body)
}

// TestGateVideoRetrieveSettledCostMatrix 锁定 provider-local 结算 sidecar：
// 只有 terminal + settled + 有限非负十进制文本存在；文本必须逐字保留。
func TestGateVideoRetrieveSettledCostMatrix(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		billingStatus string
		billedCost    string
		wantCost      string
	}{
		{"completed positive exact", "completed", "settled", "1.0800000001", "1.0800000001"},
		{"completed zero exact", "completed", "settled", "0.0000000000", "0.0000000000"},
		{"failed positive exact", "failed", "settled", "0.5000000000", "0.5000000000"},
		{"failed zero", "failed", "settled", "0", "0"},
		{"pending settled", "pending", "settled", "1.0", ""},
		{"in progress settled", "in_progress", "settled", "1.0", ""},
		{"completed pre deducted", "completed", "pre_deducted", "1.0", ""},
		{"failed pre deducted", "failed", "pre_deducted", "1.0", ""},
		{"missing billing status", "completed", "", "1.0", ""},
		{"unknown billing status", "completed", "charged", "1.0", ""},
		{"missing cost", "completed", "settled", "", ""},
		{"invalid cost", "completed", "settled", "abc", ""},
		{"negative cost", "completed", "settled", "-0.1", ""},
		{"positive infinity", "completed", "settled", "Inf", ""},
		{"negative infinity", "completed", "settled", "-Inf", ""},
		{"nan", "completed", "settled", "NaN", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, gateRetrieveEnvelope("j1", tt.status, tt.billingStatus, tt.billedCost, tt.status == "completed" || tt.status == "failed"))
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := testCtx()
			resp, bifrostErr := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
			if bifrostErr != nil {
				t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
			}
			if tt.status == "failed" && (resp.Error == nil || resp.Error.Code != "failed") {
				t.Fatalf("failed response error = %+v", resp.Error)
			}
			if tt.status == "failed" && len(resp.Videos) != 0 {
				t.Fatalf("failed response leaked download URL: %+v", resp.Videos)
			}
			taskID, cost, currency, ok := SettledVideoBillingFromContext(ctx)
			if tt.wantCost == "" {
				if ok || taskID != "" || cost != "" || currency != "" {
					t.Fatalf("billing = task=%q cost=%q currency=%q ok=%v; want absent", taskID, cost, currency, ok)
				}
				return
			}
			if !ok || taskID != "j1:gate" || cost != tt.wantCost || currency != "" {
				t.Fatalf("billing = task=%q cost=%q currency=%q ok=%v; want exact %q", taskID, cost, currency, ok, tt.wantCost)
			}
		})
	}
}

// TestGateVideoRetrieveDirectURLFailureKeepsResult 锁定：completed 任务的
// 直链获取失败只让 Videos 留空，不改写状态与可信 settled 金额。
func TestGateVideoRetrieveDirectURLFailureKeepsResult(t *testing.T) {
	tests := []struct {
		name        string
		contentCode int
		location    string
	}{
		{"content not ready", http.StatusConflict, ""},
		{"content ok body instead of redirect", http.StatusOK, ""},
		{"redirect without location", http.StatusFound, ""},
		{"non https location", http.StatusFound, "http://cdn.example.com/v.mp4"},
		{"relative location", http.StatusFound, "/v.mp4"},
		{"location with userinfo", http.StatusFound, "https://user@cdn.example.com/v.mp4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/content") {
					if tt.location != "" {
						w.Header().Set("Location", tt.location)
					}
					w.WriteHeader(tt.contentCode)
					return
				}
				fmt.Fprint(w, gateRetrieveEnvelope("j1", "completed", "settled", "1.0800000001", true))
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := testCtx()
			resp, bifrostErr := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
			if bifrostErr != nil {
				t.Fatalf("direct URL failure must not fail retrieve, got %+v", bifrostErr.Error)
			}
			if resp.Status != schemas.VideoStatusCompleted {
				t.Fatalf("Status = %q, want completed", resp.Status)
			}
			if len(resp.Videos) != 0 {
				t.Fatalf("Videos = %+v, want empty on direct URL failure", resp.Videos)
			}
			if _, cost, _, ok := SettledVideoBillingFromContext(ctx); !ok || cost != "1.0800000001" {
				t.Fatalf("settled billing = cost=%q ok=%v, want exact positive amount kept", cost, ok)
			}
		})
	}
}

// TestGateOperationGating 锁定具名实例合同：GetProviderKey 返回自定义名，
// 未授权的操作在发网前 fail-closed。
func TestGateOperationGating(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"j1","status":"pending"}}`)
	}))
	defer server.Close()

	provider, err := NewGateProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 5,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "gate-east",
			BaseProviderType:  schemas.Gate,
			AllowedRequests:   &schemas.AllowedRequests{VideoGeneration: true},
		},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewGateProvider() error = %v", err)
	}

	if got := provider.GetProviderKey(); got != "gate-east" {
		t.Fatalf("GetProviderKey() = %q, want custom instance name", got)
	}

	ctx := testCtx()
	resp, bifrostErr := provider.VideoGeneration(ctx, testKey("s"), &schemas.BifrostVideoGenerationRequest{
		Model: "m", Input: &schemas.VideoGenerationInput{Prompt: "p"},
	})
	if bifrostErr != nil {
		t.Fatalf("allowed VideoGeneration() error = %+v", bifrostErr.Error)
	}
	// 任务 ID 后缀跟随具名实例，不串实例。
	if resp.ID != "j1:gate-east" {
		t.Fatalf("ID = %q, want :gate-east suffix", resp.ID)
	}

	if _, err := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate-east"}); err == nil {
		t.Fatal("disallowed VideoRetrieve must fail closed")
	}
	if _, err := provider.VideoDownload(ctx, testKey("s"), &schemas.BifrostVideoDownloadRequest{ID: "j1:gate-east"}); err == nil {
		t.Fatal("disallowed VideoDownload must fail closed")
	}
	// 被准入拒绝后，sidecar 不得残留上一次提交账单。
	if _, _, ok := VideoSubmissionBillingFromContext(ctx); ok {
		t.Fatal("rejected call leaked submission billing sidecar")
	}
}

func TestGateVideoRetrieveNonTerminalStripsTerminalFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, gateRetrieveEnvelope("j1", "in_progress", "settled", "1.0", true))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := testCtx()
	resp, bifrostErr := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}
	if resp.Status != schemas.VideoStatusInProgress {
		t.Fatalf("Status = %q", resp.Status)
	}
	if resp.CompletedAt != nil || resp.ExpiresAt != nil || len(resp.Videos) != 0 {
		t.Fatalf("non-terminal response leaked terminal fields: %+v", resp)
	}
	if taskID, cost, currency, ok := SettledVideoBillingFromContext(ctx); ok || taskID != "" || cost != "" || currency != "" {
		t.Fatalf("non-terminal response leaked settled billing: task=%q cost=%q currency=%q ok=%v", taskID, cost, currency, ok)
	}
}

func TestGateVideoRetrieveRejectsWrongJobID(t *testing.T) {
	for _, jobID := range []string{"", "other"} {
		t.Run(fmt.Sprintf("job_id_%q", jobID), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, gateRetrieveEnvelope(jobID, "completed", "settled", "1.0", true))
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := testCtx()
			_, bifrostErr := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
			if bifrostErr == nil {
				t.Fatalf("job_id %q must fail closed", jobID)
			}
			if taskID, cost, currency, ok := SettledVideoBillingFromContext(ctx); ok || taskID != "" || cost != "" || currency != "" {
				t.Fatalf("rejected identity leaked billing: task=%q cost=%q currency=%q ok=%v", taskID, cost, currency, ok)
			}
		})
	}
}

// TestGateVideoRetrieveClearsStaleSettledCost 重用同一个 context：任何后续
// 非结算结果或错误都必须先清掉旧 sidecar，不能让调用方读到上次成功的费用。
func TestGateVideoRetrieveClearsStaleSettledCost(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		emptyID    bool
	}{
		{"nonterminal", http.StatusOK, gateRetrieveEnvelope("j1", "in_progress", "settled", "9.9", true), false},
		{"pre deducted", http.StatusOK, gateRetrieveEnvelope("j1", "completed", "pre_deducted", "9.9", true), false},
		{"http error", http.StatusInternalServerError, `{"code":500,"msg":"boom"}`, false},
		{"bad envelope", http.StatusOK, `{"code":429,"msg":"slow down"}`, false},
		{"missing envelope data", http.StatusOK, `{"code":200,"msg":""}`, false},
		{"bad json", http.StatusOK, `{`, false},
		{"unknown status", http.StatusOK, gateRetrieveEnvelope("j1", "teleporting", "settled", "9.9", true), false},
		{"invalid timestamp", http.StatusOK, strings.Replace(gateRetrieveEnvelope("j1", "completed", "settled", "9.9", true), "2026-05-27T05:00:00Z", "not-a-time", 1), false},
		{"wrong job id", http.StatusOK, gateRetrieveEnvelope("other", "completed", "settled", "9.9", true), false},
		{"empty response job id", http.StatusOK, gateRetrieveEnvelope("", "completed", "settled", "9.9", true), false},
		{"empty request id", http.StatusOK, gateRetrieveEnvelope("j1", "completed", "settled", "9.9", true), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					fmt.Fprint(w, gateRetrieveEnvelope("j1", "completed", "settled", "1.0000000001", true))
					return
				}
				w.WriteHeader(tt.statusCode)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			provider := newTestProvider(t, server.URL)
			ctx := testCtx()
			if _, bifrostErr := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"}); bifrostErr != nil {
				t.Fatalf("seed retrieve error = %+v", bifrostErr.Error)
			}
			if taskID, cost, currency, ok := SettledVideoBillingFromContext(ctx); !ok || taskID != "j1:gate" || cost != "1.0000000001" || currency != "" {
				t.Fatalf("seed billing = task=%q cost=%q currency=%q ok=%v", taskID, cost, currency, ok)
			}

			requestID := "j1:gate"
			if tt.emptyID {
				requestID = ""
			}
			_, _ = provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: requestID})
			if taskID, cost, currency, ok := SettledVideoBillingFromContext(ctx); ok || taskID != "" || cost != "" || currency != "" {
				t.Fatalf("stale billing survived: task=%q cost=%q currency=%q ok=%v", taskID, cost, currency, ok)
			}
		})
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

	ctx := testCtx()
	resp, bifrostErr := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}
	raw, ok := resp.ExtraFields.RawResponse.(json.RawMessage)
	if !ok || !strings.Contains(string(raw), `"billed_cost":"1.0800000001"`) {
		t.Fatalf("raw response must preserve exact billed_cost string, got %T %+v", resp.ExtraFields.RawResponse, resp.ExtraFields.RawResponse)
	}
	if taskID, cost, currency, ok := SettledVideoBillingFromContext(ctx); !ok || taskID != "j1:gate" || cost != "1.0800000001" || currency != "" {
		t.Fatalf("RawResponse changed settled billing: task=%q cost=%q currency=%q ok=%v", taskID, cost, currency, ok)
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

func TestGateVideoRetrieveUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"code":401,"msg":"鉴权失败"}`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	_, bifrostErr := provider.VideoRetrieve(testCtx(), testKey("bad-secret"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr == nil {
		t.Fatal("VideoRetrieve() expected error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %v, want 401", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, "鉴权失败") {
		t.Fatalf("Message = %+v", bifrostErr.Error)
	}
}

// gateDownloadFakeAPI 返回始终 302 到给定 CDN 的第一跳 fake server。
func gateDownloadFakeAPI(t *testing.T, location string, record *recordedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if record != nil {
			record.method = r.Method
			record.path = r.URL.Path
			record.authorization = r.Header.Get("Authorization")
			record.idempotencyKey = r.Header.Get("Idempotency-Key")
			record.providerSecret = r.Header.Get("X-Provider-Secret")
			record.contextSecret = r.Header.Get("X-Context-Secret")
		}
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	}))
}

// readLargeResponseFromCtx 读取并关闭 large-response context 中的流，返回完整字节。
func readLargeResponseFromCtx(t *testing.T, ctx *schemas.BifrostContext) []byte {
	t.Helper()
	reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(*providerUtils.LargeResponseReader)
	if !ok || reader == nil {
		t.Fatal("large response reader not registered in context")
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return got
}

// TestGateVideoDownloadTwoHopStreamed 验证阈值开启时的两跳下载：第一跳携带
// Bearer 且不自动跟随重定向；第二跳是全新请求，绝不携带 Authorization /
// Idempotency-Key；超限响应经既有 large-response context 契约完整流出。
func TestGateVideoDownloadTwoHopStreamed(t *testing.T) {
	payload := testPayload(2 * 1024 * 1024)

	var cdnAuth, cdnIdempotency, cdnProviderSecret, cdnContextSecret string
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnAuth = r.Header.Get("Authorization")
		cdnIdempotency = r.Header.Get("Idempotency-Key")
		cdnProviderSecret = r.Header.Get("X-Provider-Secret")
		cdnContextSecret = r.Header.Get("X-Context-Secret")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Write(payload)
	}))
	defer cdn.Close()

	var hop1 recordedRequest
	api := gateDownloadFakeAPI(t, cdn.URL+"/videos/video_abc123.mp4?expires=1780000000", &hop1)
	defer api.Close()

	provider := newTestProvider(t, api.URL)
	provider.networkConfig.ExtraHeaders = map[string]string{"X-Provider-Secret": "provider-only"}
	ctx := testCtx()
	// 幂等键经 ExtraHeaders 契约传递；第二跳是全新请求，绝不携带它。
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"Idempotency-Key":  {"llmgw-exec-42"},
		"X-Context-Secret": {"context-only"},
	})
	// 配置大响应阈值：2 MiB payload 超过 1 MiB 阈值，必须走流式契约。
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(1*1024*1024))

	resp, bifrostErr := provider.VideoDownload(ctx, testKey("gate-secret"), &schemas.BifrostVideoDownloadRequest{
		Provider: schemas.Gate,
		ID:       "video_abc123:gate",
	})
	if bifrostErr != nil {
		t.Fatalf("VideoDownload() error = %+v", bifrostErr.Error)
	}

	if hop1.authorization != "Bearer gate-secret" {
		t.Fatalf("hop-1 Authorization = %q", hop1.authorization)
	}
	if hop1.method != http.MethodGet || hop1.path != "/api/v1/videos/video_abc123/content" {
		t.Fatalf("hop-1 request = %s %s", hop1.method, hop1.path)
	}
	if hop1.idempotencyKey != "llmgw-exec-42" || hop1.providerSecret != "provider-only" || hop1.contextSecret != "context-only" {
		t.Fatalf("hop-1 headers = %+v", hop1)
	}
	if cdnAuth != "" || cdnIdempotency != "" || cdnProviderSecret != "" || cdnContextSecret != "" {
		t.Fatalf("hop-2 leaked headers: Authorization=%q Idempotency-Key=%q provider=%q context=%q",
			cdnAuth, cdnIdempotency, cdnProviderSecret, cdnContextSecret)
	}
	// 元数据响应不携带字节；实际流走 large-response context 契约。
	if len(resp.Content) != 0 {
		t.Fatalf("Content must stay empty, got %d bytes", len(resp.Content))
	}
	// 先完整读取并关闭流再做断言：断言失败也不能泄漏连接。
	got := readLargeResponseFromCtx(t, ctx)
	if !bytes.Equal(got, payload) {
		t.Fatalf("streamed %d bytes, want %d (integrity mismatch)", len(got), len(payload))
	}
	if mode, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); !mode {
		t.Fatal("large response mode not set in context")
	}
	if ct, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseContentType).(string); ct != "video/mp4" {
		t.Fatalf("registered content type = %q", ct)
	}
	if resp.VideoID != "video_abc123:gate" || resp.ContentType != "video/mp4" {
		t.Fatalf("response = %+v", resp)
	}
}

// TestGateVideoDownloadLargeStreamOutlivesRequestTimeout 验证 hop2 的请求
// timeout 只保护拨号、响应头和 prefetch；reader 交接后沿用 .2 原生流合同，
// 不能把一个仍持续传输的大视频按普通请求 timeout 硬截断。
func TestGateVideoDownloadLargeStreamOutlivesRequestTimeout(t *testing.T) {
	payload := testPayload(128 * 1024)
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload[:64*1024])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(600 * time.Millisecond)
		_, _ = w.Write(payload[64*1024:])
	}))
	defer cdn.Close()

	api := gateDownloadFakeAPI(t, cdn.URL+"/v.mp4", nil)
	defer api.Close()

	provider := newTestProvider(t, api.URL)
	provider.downloadTimeout = 250 * time.Millisecond
	ctx := testCtx()
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, int64(1024))

	resp, bifrostErr := provider.VideoDownload(ctx, testKey("s"), &schemas.BifrostVideoDownloadRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("VideoDownload() error = %+v", bifrostErr.Error)
	}
	if len(resp.Content) != 0 {
		t.Fatalf("large path Content must stay empty, got %d bytes", len(resp.Content))
	}
	if got := readLargeResponseFromCtx(t, ctx); !bytes.Equal(got, payload) {
		t.Fatalf("streamed %d bytes, want %d", len(got), len(payload))
	}
}

// TestGateVideoDownloadBuffered 验证未配置阈值时的既有 buffered 语义：
// 内容经 Content []byte 返回，不向 context 注册 reader。
func TestGateVideoDownloadBuffered(t *testing.T) {
	payload := testPayload(128 * 1024)

	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Write(payload)
	}))
	defer cdn.Close()

	api := gateDownloadFakeAPI(t, cdn.URL+"/v.mp4", nil)
	defer api.Close()

	provider := newTestProvider(t, api.URL)
	ctx := testCtx()
	resp, bifrostErr := provider.VideoDownload(ctx, testKey("gate-secret"), &schemas.BifrostVideoDownloadRequest{
		Provider: schemas.Gate,
		ID:       "video_abc123:gate",
	})
	if bifrostErr != nil {
		t.Fatalf("VideoDownload() error = %+v", bifrostErr.Error)
	}
	if !bytes.Equal(resp.Content, payload) {
		t.Fatalf("buffered Content = %d bytes, want %d (integrity mismatch)", len(resp.Content), len(payload))
	}
	if resp.ContentType != "video/mp4" {
		t.Fatalf("ContentType = %q", resp.ContentType)
	}
	if ctx.Value(schemas.BifrostContextKeyLargeResponseReader) != nil {
		t.Fatal("buffered path must not register a large response reader")
	}
}

// serveChunked 强制 chunked 传输（无 Content-Length）。
func serveChunked(payload []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		half := len(payload) / 2
		_, _ = w.Write(payload[:half])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write(payload[half:])
	}
}

// serveGzip 以 gzip Content-Encoding + 显式 Content-Length 返回压缩体。
func serveGzip(payload []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write(payload)
		_ = gw.Close()
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		_, _ = w.Write(buf.Bytes())
	}
}

// serveRawNoContentType 经 hijack 裸写响应，真正缺失 Content-Type 头
// （Go net/http 否则会嗅探补一个）。
func serveRawNoContentType(payload []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			panic("test server does not support hijack")
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		fmt.Fprintf(rw, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(payload))
		_, _ = rw.Write(payload)
		_ = rw.Flush()
	}
}

// TestGateVideoDownloadLargeResponsePaths 覆盖 buffered 与 threshold streaming
// 两条下载路径：显式 Content-Length 上/下阈值、chunked 上/下阈值、gzip、
// 以及默认 MIME 补全。
func TestGateVideoDownloadLargeResponsePaths(t *testing.T) {
	big := testPayload(64 * 1024)
	small := testPayload(512)

	tests := []struct {
		name            string
		threshold       int64
		serve           http.HandlerFunc
		wantPayload     []byte
		wantLarge       bool
		wantContentType string
	}{
		{
			name:      "explicit content-length above threshold streams",
			threshold: 1024,
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", strconv.Itoa(len(big)))
				w.Write(big)
			},
			wantPayload:     big,
			wantLarge:       true,
			wantContentType: "video/mp4",
		},
		{
			name:      "explicit content-length within threshold buffers",
			threshold: 1 << 20,
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", strconv.Itoa(len(small)))
				w.Write(small)
			},
			wantPayload:     small,
			wantLarge:       false,
			wantContentType: "video/mp4",
		},
		{
			name:            "chunked within threshold buffers",
			threshold:       1 << 20,
			serve:           serveChunked(small),
			wantPayload:     small,
			wantLarge:       false,
			wantContentType: "video/mp4",
		},
		{
			name:            "chunked above threshold streams",
			threshold:       1024,
			serve:           serveChunked(big),
			wantPayload:     big,
			wantLarge:       true,
			wantContentType: "video/mp4",
		},
		{
			name:            "gzip above threshold streams decompressed",
			threshold:       1024,
			serve:           serveGzip(big),
			wantPayload:     big,
			wantLarge:       true,
			wantContentType: "video/mp4",
		},
		{
			name:            "missing content-type defaults to video/mp4 (streamed)",
			threshold:       1024,
			serve:           serveRawNoContentType(big),
			wantPayload:     big,
			wantLarge:       true,
			wantContentType: "video/mp4",
		},
		{
			name:            "missing content-type defaults to video/mp4 (buffered)",
			threshold:       1 << 20,
			serve:           serveRawNoContentType(small),
			wantPayload:     small,
			wantLarge:       false,
			wantContentType: "video/mp4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cdn := httptest.NewTLSServer(tt.serve)
			defer cdn.Close()

			api := gateDownloadFakeAPI(t, cdn.URL+"/v.mp4", nil)
			defer api.Close()

			provider := newTestProvider(t, api.URL)
			ctx := testCtx()
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseThreshold, tt.threshold)

			resp, bifrostErr := provider.VideoDownload(ctx, testKey("s"), &schemas.BifrostVideoDownloadRequest{
				Provider: schemas.Gate,
				ID:       "j1:gate",
			})
			if bifrostErr != nil {
				t.Fatalf("VideoDownload() error = %+v", bifrostErr.Error)
			}
			if tt.wantLarge {
				if len(resp.Content) != 0 {
					t.Fatalf("large path Content must stay empty, got %d bytes", len(resp.Content))
				}
				// 先完整读取并关闭流再做断言：断言失败也不能泄漏连接。
				got := readLargeResponseFromCtx(t, ctx)
				if !bytes.Equal(got, tt.wantPayload) {
					t.Fatalf("streamed %d bytes, want %d (integrity mismatch)", len(got), len(tt.wantPayload))
				}
				if ct, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseContentType).(string); ct != tt.wantContentType {
					t.Fatalf("context content type = %q, want %q", ct, tt.wantContentType)
				}
				if resp.ContentType != tt.wantContentType {
					t.Fatalf("ContentType = %q, want %q", resp.ContentType, tt.wantContentType)
				}
				return
			}
			if resp.ContentType != tt.wantContentType {
				t.Fatalf("ContentType = %q, want %q", resp.ContentType, tt.wantContentType)
			}

			if !bytes.Equal(resp.Content, tt.wantPayload) {
				t.Fatalf("buffered Content = %d bytes, want %d (integrity mismatch)", len(resp.Content), len(tt.wantPayload))
			}
			if ctx.Value(schemas.BifrostContextKeyLargeResponseReader) != nil {
				t.Fatal("buffered path must not register a large response reader")
			}
		})
	}
}

func TestGateVideoDownloadStatusMapping(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		wantCode   string
	}{
		{"unauthorized", http.StatusUnauthorized, "gate_error"},
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

// TestGateVideoDownloadRejectsNon302 验证第一跳只接受 302：200 直出内容、
// 301/307 等其他 3xx 一律 fail-closed，且不触达 CDN。
func TestGateVideoDownloadRejectsNon302(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
	}{
		{"direct 200 rejected", http.StatusOK},
		{"301 rejected", http.StatusMovedPermanently},
		{"307 rejected", http.StatusTemporaryRedirect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cdnHit := false
			cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cdnHit = true
			}))
			defer cdn.Close()

			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", cdn.URL+"/v.mp4")
				w.WriteHeader(tt.httpStatus)
				fmt.Fprint(w, `{"code":200,"msg":"","data":{}}`)
			}))
			defer api.Close()

			provider := newTestProvider(t, api.URL)
			_, bifrostErr := provider.VideoDownload(testCtx(), testKey("s"), &schemas.BifrostVideoDownloadRequest{ID: "j1:gate"})
			if bifrostErr == nil {
				t.Fatalf("VideoDownload() HTTP %d must be rejected", tt.httpStatus)
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tt.httpStatus {
				t.Fatalf("StatusCode = %v, want %d", bifrostErr.StatusCode, tt.httpStatus)
			}
			if cdnHit {
				t.Fatal("CDN must not be contacted when hop-1 is not 302")
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

// TestGateVideoDownloadSSRFRejected 使用生产 downloadClient（SSRF-safe
// dialer 未被替换），验证指向私网地址的 HTTPS Location 在拨号阶段被拒绝。
func TestGateVideoDownloadSSRFRejected(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://127.0.0.1:1/loopback.mp4")
		w.WriteHeader(http.StatusFound)
	}))
	defer api.Close()

	provider, err := NewGateProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        api.URL,
			DefaultRequestTimeoutInSeconds: 5,
			MaxConnsPerHost:                4,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
	}, testLogger{})
	if err != nil {
		t.Fatal(err)
	}

	_, bifrostErr := provider.VideoDownload(testCtx(), testKey("s"), &schemas.BifrostVideoDownloadRequest{ID: "j1:gate"})
	if bifrostErr == nil {
		t.Fatal("VideoDownload() expected SSRF rejection")
	}
	if !strings.Contains(fmt.Sprintf("%+v", bifrostErr), "non-public address") {
		t.Fatalf("expected non-public address rejection, got %+v", bifrostErr)
	}
}

// TestGateVideoDownloadHop2ErrorBoundedWithoutThreshold 锁定默认路径：即使调用方
// 没配置 large-response threshold，CDN 错误体也只能读取 4 KiB，并立即拆掉
// 半读连接。服务端若看不到断连，本测试直接失败，不能再用 10 秒 Close 假绿。
func TestGateVideoDownloadHop2ErrorBoundedWithoutThreshold(t *testing.T) {
	started := make(chan struct{})
	disconnected := make(chan struct{})
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(1<<30))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 32<<10))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		close(disconnected)
	}))
	t.Cleanup(func() {
		cdn.CloseClientConnections()
		cdn.Close()
	})

	api := gateDownloadFakeAPI(t, cdn.URL+"/v.mp4", nil)
	defer api.Close()

	provider := newTestProviderWithTimeout(t, api.URL, 3)
	start := time.Now()
	_, bifrostErr := provider.VideoDownload(testCtx(), testKey("s"), &schemas.BifrostVideoDownloadRequest{ID: "j1:gate"})
	if bifrostErr == nil {
		t.Fatal("VideoDownload() expected hop-2 error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("StatusCode = %v, want 500", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || len(bifrostErr.Error.Message) > 4096 {
		t.Fatalf("error message must be bounded, got %d bytes", len(bifrostErr.Error.Message))
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("bounded error path took %v; likely buffered the full body", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("CDN did not start streaming the error body")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("CDN did not observe prompt client disconnect")
	}
}

func TestGateVideoDownloadHop2CancellationAndDeadline(t *testing.T) {
	tests := []struct {
		name       string
		newContext func() (*schemas.BifrostContext, context.CancelFunc)
		cancelNow  bool
	}{
		{
			name: "cancel",
			newContext: func() (*schemas.BifrostContext, context.CancelFunc) {
				return schemas.NewBifrostContextWithCancel(context.Background())
			},
			cancelNow: true,
		},
		{
			name: "deadline",
			newContext: func() (*schemas.BifrostContext, context.CancelFunc) {
				return schemas.NewBifrostContextWithTimeout(context.Background(), 150*time.Millisecond)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{})
			disconnected := make(chan struct{})
			cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", strconv.Itoa(1<<30))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(testPayload(32 << 10))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				close(started)
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
				close(disconnected)
			}))
			t.Cleanup(func() {
				cdn.CloseClientConnections()
				cdn.Close()
			})

			api := gateDownloadFakeAPI(t, cdn.URL+"/v.mp4", nil)
			defer api.Close()
			provider := newTestProviderWithTimeout(t, api.URL, 1)
			ctx, cancel := tt.newContext()
			defer cancel()

			type result struct{ err *schemas.BifrostError }
			resultCh := make(chan result, 1)
			start := time.Now()
			go func() {
				_, bifrostErr := provider.VideoDownload(ctx, testKey("s"), &schemas.BifrostVideoDownloadRequest{ID: "j1:gate"})
				resultCh <- result{err: bifrostErr}
			}()

			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("hop-2 request did not reach CDN")
			}
			if tt.cancelNow {
				cancel()
			}

			select {
			case got := <-resultCh:
				if got.err == nil {
					t.Fatal("VideoDownload() returned success after interrupted body")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("VideoDownload() ignored cancellation/deadline")
			}
			if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
				t.Fatalf("VideoDownload() returned after %v", elapsed)
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("CDN did not observe connection teardown")
			}
		})
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
