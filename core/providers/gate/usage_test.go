package gate

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestGateVideoUsageNormalization(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		resolution string
		ratio      string
		duration   int
		reported   string
		want       int
		raw        string
		source     string
	}{
		{"87300 sample", "bytedance/seedance-2.0", "720p", "9:16", 4, "", 87300, "87300", "model_rule"},
		{"731025 sample", "bytedance/seedance-2.0", "1080p", "9:16", 15, "", 731025, "731025", "model_rule"},
		{"ceil fractional token", "bytedance/seedance-2.0", "480p", "16:9", 4, "", 40595, "40594.5", "model_rule"},
		{"real square not representative size", "bytedance/seedance-2.0", "720p", "1:1", 4, "", 87300, "87300", "model_rule"},
		{"explicit output before model rule", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"output_tokens":42,"total_tokens":99}`, 42, "42", "provider_reported"},
		{"explicit completion", "other-model", "", "", 0, `{"completion_tokens":18}`, 18, "18", "provider_reported"},
		{"explicit zero", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"completion_tokens":0}`, 0, "0", "provider_reported"},
		{"both explicit names agree", "other-model", "", "", 0, `{"completion_tokens":2,"output_tokens":2}`, 2, "2", "provider_reported"},
		{"total is not output", "other-model", "", "", 0, `{"total_tokens":99}`, 0, "", ""},
		{"total does not suppress model rule", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"total_tokens":99}`, 87300, "87300", "model_rule"},
		{"null usage is absent", "bytedance/seedance-2.0", "720p", "9:16", 4, `null`, 87300, "87300", "model_rule"},
		{"conflicting output", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"completion_tokens":2,"output_tokens":3}`, 0, "", ""},
		{"negative output", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"output_tokens":-1}`, 0, "", ""},
		{"fractional output", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"output_tokens":1.5}`, 0, "", ""},
		{"string output", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"output_tokens":"18"}`, 0, "", ""},
		{"null output", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"output_tokens":null}`, 0, "", ""},
		{"overflow output", "bytedance/seedance-2.0", "720p", "9:16", 4, `{"output_tokens":999999999999999999999}`, 0, "", ""},
		{"malformed usage preserves result", "bytedance/seedance-2.0", "720p", "9:16", 4, `[]`, 0, "", ""},
		{"unknown model", "other-model", "720p", "9:16", 4, "", 0, "", ""},
		{"fast is independent model", "bytedance/seedance-2.0-fast", "720p", "9:16", 4, "", 0, "", ""},
		{"missing duration", "bytedance/seedance-2.0", "720p", "9:16", 0, "", 0, "", ""},
		{"negative duration", "bytedance/seedance-2.0", "720p", "9:16", -1, "", 0, "", ""},
		{"overflow frames", "bytedance/seedance-2.0", "720p", "9:16", math.MaxInt, "", 0, "", ""},
		{"overflow tokens", "bytedance/seedance-2.0", "4k", "16:9", math.MaxInt / 24, "", 0, "", ""},
		{"unknown ratio", "bytedance/seedance-2.0", "720p", "9:21", 4, "", 0, "", ""},
		{"adaptive has no exact dimensions", "bytedance/seedance-2.0", "720p", "adaptive", 4, "", 0, "", ""},
		{"1024 has no table", "bytedance/seedance-2.0", "1024p", "9:16", 4, "", 0, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage, metadata := normalizeVideoUsage(&GateVideoRetrieveData{
				Model: tt.model, Resolution: tt.resolution, AspectRatio: tt.ratio, Duration: tt.duration,
				Usage: json.RawMessage(tt.reported), GenerateAudio: schemas.Ptr(false),
			})
			if tt.source == "" {
				if usage != nil {
					t.Fatalf("unexpected usage: %#v", usage)
				}
			} else if usage == nil || usage.CompletionTokens != tt.want || usage.Cost != nil {
				t.Fatalf("usage = %#v, want %d without cost", usage, tt.want)
			}
			if metadata.Source != tt.source || metadata.RawOutputTokens != tt.raw || metadata.RoundedOutputTokens != tt.want {
				t.Fatalf("metadata = %#v", metadata)
			}
			if metadata.Duration != tt.duration || metadata.Resolution != tt.resolution || metadata.AspectRatio != tt.ratio || metadata.GenerateAudio == nil || *metadata.GenerateAudio {
				t.Fatalf("terminal facts changed: %#v", metadata)
			}
			if tt.source == "model_rule" && (metadata.RuleVersion == "" || metadata.Frames != int64(tt.duration)*24+1 || metadata.Width <= 0 || metadata.Height <= 0) {
				t.Fatalf("missing rule evidence: %#v", metadata)
			}
		})
	}
}

func TestGateSeedance20SpecificationTable(t *testing.T) {
	// 独立金样固定完整规格表和四秒输出 Token，尤其保护非 16:9 的真实像素。
	tests := []struct {
		resolution, ratio     string
		width, height, tokens int
	}{
		{"480p", "16:9", 864, 496, 40595},
		{"480p", "4:3", 752, 560, 39892},
		{"480p", "1:1", 640, 640, 38800},
		{"480p", "3:4", 560, 752, 39892},
		{"480p", "9:16", 496, 864, 40595},
		{"480p", "21:9", 992, 432, 40595},
		{"720p", "16:9", 1280, 720, 87300},
		{"720p", "4:3", 1112, 834, 87851},
		{"720p", "1:1", 960, 960, 87300},
		{"720p", "3:4", 834, 1112, 87851},
		{"720p", "9:16", 720, 1280, 87300},
		{"720p", "21:9", 1470, 630, 87727},
		{"1080p", "16:9", 1920, 1080, 196425},
		{"1080p", "4:3", 1664, 1248, 196716},
		{"1080p", "1:1", 1440, 1440, 196425},
		{"1080p", "3:4", 1248, 1664, 196716},
		{"1080p", "9:16", 1080, 1920, 196425},
		{"1080p", "21:9", 2206, 946, 197683},
		{"4k", "16:9", 3840, 2160, 785700},
		{"4k", "4:3", 3326, 2494, 785762},
		{"4k", "1:1", 2880, 2880, 785700},
		{"4k", "3:4", 2494, 3326, 785762},
		{"4k", "9:16", 2160, 3840, 785700},
		{"4k", "21:9", 4398, 1886, 785722},
	}
	for _, tt := range tests {
		t.Run(tt.resolution+"/"+tt.ratio, func(t *testing.T) {
			if _, err := ToGateVideoGenerationRequest(&schemas.BifrostVideoGenerationRequest{
				Model: "bytedance/seedance-2.0", Input: &schemas.VideoGenerationInput{Prompt: "test"},
				Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{"resolution": tt.resolution, "aspect_ratio": tt.ratio}},
			}); err != nil {
				t.Fatalf("confirmed specification rejected: %v", err)
			}
			usage, metadata := normalizeVideoUsage(&GateVideoRetrieveData{
				Model: "bytedance/seedance-2.0", Resolution: tt.resolution, AspectRatio: tt.ratio, Duration: 4,
			})
			if usage == nil || usage.CompletionTokens != tt.tokens || metadata.Width != tt.width || metadata.Height != tt.height {
				t.Fatalf("specification = %#v, %#v; want %dx%d, %d tokens", usage, metadata, tt.width, tt.height, tt.tokens)
			}
		})
	}
}

func TestGateVideoUsageContextIdentityAndClearing(t *testing.T) {
	response := `{"job_id":"task","status":"completed","model":"bytedance/seedance-2.0","duration":4,"resolution":"720p","aspect_ratio":"9:16","generate_audio":false,"billing_status":"settled","billed_cost":"1.08"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"code":200,"data":%s}`, response)
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	ctx := testCtx()
	result, err := provider.VideoRetrieve(ctx, testKey("local-test"), &schemas.BifrostVideoRetrieveRequest{ID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	id, usage, metadata, ok := VideoUsageFromContext(ctx)
	if !ok || id != result.ID || usage.CompletionTokens != 87300 || metadata.GenerateAudio == nil || *metadata.GenerateAudio {
		t.Fatalf("context = %q %#v %#v %v", id, usage, metadata, ok)
	}
	if result.Size != "720x1280" || result.Seconds == nil || *result.Seconds != "4" || result.Usage == nil || result.Usage.Cost.TotalCost != 1.08 {
		t.Fatalf("existing result projection changed: %#v", result)
	}
	usage.CompletionTokens = 1
	*metadata.GenerateAudio = true
	_, reloaded, reloadedMetadata, _ := VideoUsageFromContext(ctx)
	if reloaded.CompletionTokens != 87300 || *reloadedMetadata.GenerateAudio {
		t.Fatal("accessor leaked mutable context fields")
	}
	for _, next := range []struct {
		name, body string
		wantErr    bool
	}{
		{"wrong task", `{"job_id":"other","status":"completed"}`, true},
		{"pending", `{"job_id":"task","status":"pending"}`, false},
		{"failed", `{"job_id":"task","status":"failed"}`, false},
		{"invalid timestamp", `{"job_id":"task","status":"completed","completed_at":"invalid"}`, true},
	} {
		t.Run(next.name, func(t *testing.T) {
			setVideoUsage(ctx, id, reloaded, reloadedMetadata)
			response = next.body
			_, err := provider.VideoRetrieve(ctx, testKey("local-test"), &schemas.BifrostVideoRetrieveRequest{ID: "task"})
			if (err != nil) != next.wantErr {
				t.Fatalf("error = %v", err)
			}
			if _, _, _, ok := VideoUsageFromContext(ctx); ok {
				t.Fatal("previous task usage survived")
			}
		})
	}
	if _, _, _, ok := VideoUsageFromContext(nil); ok {
		t.Fatal("nil context returned usage")
	}
}

func TestGateVideoUsageConcurrentContexts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, gateVideosPath+"/")
		n, _ := strconv.Atoi(id)
		fmt.Fprintf(w, `{"code":200,"data":{"job_id":%q,"status":"completed","model":"other-model","usage":{"completion_tokens":%d}}}`, id, n)
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	var workers sync.WaitGroup
	for n := 0; n < 20; n++ {
		workers.Go(func() {
			ctx := testCtx()
			result, err := provider.VideoRetrieve(ctx, testKey("local-test"), &schemas.BifrostVideoRetrieveRequest{ID: strconv.Itoa(n)})
			if err != nil {
				t.Errorf("retrieve %d: %v", n, err)
				return
			}
			id, usage, _, ok := VideoUsageFromContext(ctx)
			if !ok || id != result.ID || usage == nil || usage.CompletionTokens != n {
				t.Errorf("cross-task usage %d: %s %#v %v", n, id, usage, ok)
			}
		})
	}
	workers.Wait()
}
