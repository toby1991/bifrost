package gate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestGateVideoSpecificationPreservesModelAndDefaultBoundaries(t *testing.T) {
	tests := []struct {
		name, model, resolution, ratio string
	}{
		{"no explicit specification", "bytedance/seedance-2.0", "", ""},
		{"resolution only", "bytedance/seedance-2.0", "1024p", ""},
		{"ratio only", "bytedance/seedance-2.0", "", "adaptive"},
		{"different model", "other-model", "1024p", "9:21"},
		{"fast is independent model", "bytedance/seedance-2.0-fast", "1024p", "9:21"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := &schemas.BifrostVideoGenerationRequest{
				Model: tt.model, Input: &schemas.VideoGenerationInput{Prompt: "test"},
				Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{"resolution": tt.resolution, "aspect_ratio": tt.ratio}},
			}
			got, err := ToGateVideoGenerationRequest(request)
			if err != nil || got.Resolution != tt.resolution || got.AspectRatio != tt.ratio {
				t.Fatalf("model/default boundary changed: %#v, %v", got, err)
			}
		})
	}
}

func TestGateInvalidSeedanceSpecificationDoesNotDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "must not dispatch", http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	tests := []struct {
		name, resolution, ratio string
	}{
		{"unsupported resolution", "1024p", "16:9"},
		{"unsupported portrait ratio", "720p", "9:21"},
		{"adaptive cannot be expressed exactly", "720p", "adaptive"},
		{"unknown resolution", "8k", "16:9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.VideoGeneration(testCtx(), testKey("local-test"), &schemas.BifrostVideoGenerationRequest{
				Model: "bytedance/seedance-2.0", Input: &schemas.VideoGenerationInput{Prompt: "test"},
				Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{"resolution": tt.resolution, "aspect_ratio": tt.ratio}},
			})
			if err == nil || err.Error == nil || err.Error.Error == nil || !strings.Contains(err.Error.Error.Error(), "unsupported resolution/aspect_ratio combination") {
				t.Fatalf("invalid specification error = %#v", err)
			}
			if err.Error.Code == nil || *err.Error.Code != "request_not_dispatched" {
				t.Fatalf("invalid specification must retain pre-dispatch error code: %#v", err.Error)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid specifications dispatched %d requests", calls.Load())
	}
}
