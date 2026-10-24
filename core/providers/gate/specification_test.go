package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestGateUnifiedVideoContract(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*schemas.BifrostVideoGenerationRequest)
		wantErr bool
	}{
		{name: "complete request"},
		{name: "other model retains its own specifications", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			req.Model = "other-model"
			req.Params.ExtraParams[schemas.VideoExtraParamResolution] = "1024p"
			req.Params.ExtraParams[schemas.VideoExtraParamAspectRatio] = "9:21"
		}},
		{name: "missing params", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Params = nil }, wantErr: true},
		{name: "missing input", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Input = nil }, wantErr: true},
		{name: "missing prompt", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Input.Prompt = "" }, wantErr: true},
		{name: "missing duration", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Params.Seconds = nil }, wantErr: true},
		{name: "empty duration", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Params.Seconds = schemas.Ptr("") }, wantErr: true},
		{name: "zero duration", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Params.Seconds = schemas.Ptr("0") }, wantErr: true},
		{name: "negative duration", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Params.Seconds = schemas.Ptr("-1") }, wantErr: true},
		{name: "fractional duration", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Params.Seconds = schemas.Ptr("5.5") }, wantErr: true},
		{name: "overflow duration", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			req.Params.Seconds = schemas.Ptr("999999999999999999999999")
		}, wantErr: true},
		{name: "missing resolution", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			delete(req.Params.ExtraParams, schemas.VideoExtraParamResolution)
		}, wantErr: true},
		{name: "wrong resolution type", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			req.Params.ExtraParams[schemas.VideoExtraParamResolution] = 720
		}, wantErr: true},
		{name: "blank resolution", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			req.Params.ExtraParams[schemas.VideoExtraParamResolution] = " "
		}, wantErr: true},
		{name: "empty ratio", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			req.Params.ExtraParams[schemas.VideoExtraParamAspectRatio] = ""
		}, wantErr: true},
		{name: "wrong ratio type", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			req.Params.ExtraParams[schemas.VideoExtraParamAspectRatio] = 169
		}, wantErr: true},
		{name: "missing ratio", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			delete(req.Params.ExtraParams, schemas.VideoExtraParamAspectRatio)
		}, wantErr: true},
		{name: "unsupported model specification", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			req.Params.ExtraParams[schemas.VideoExtraParamResolution] = "1024p"
		}, wantErr: true},
		{name: "unknown extension", mutate: func(req *schemas.BifrostVideoGenerationRequest) { req.Params.ExtraParams["unknown"] = true }, wantErr: true},
		{name: "invalid media role", mutate: func(req *schemas.BifrostVideoGenerationRequest) {
			req.Params.ExtraParams[schemas.VideoExtraParamInputReferences] = []schemas.VideoMediaReference{{Type: schemas.VideoMediaTypeAudio, URL: "https://example.com/a.mp3", Role: schemas.VideoMediaRoleReference}}
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, before := unifiedGateTestRequest(), unifiedGateTestRequest()
			if test.mutate != nil {
				test.mutate(request)
				test.mutate(before)
			}
			err := ValidateUnifiedVideoGenerationRequest(request)
			if (err != nil) != test.wantErr {
				t.Fatalf("validation error = %v, want error %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(request, before) {
				t.Fatal("pure validation changed request")
			}
		})
	}
	if err := ValidateUnifiedVideoGenerationRequest(nil); err == nil {
		t.Fatal("nil unified request accepted")
	}
}

func TestGateUnifiedStrictnessDoesNotChangeLegacyDefaults(t *testing.T) {
	request := &schemas.BifrostVideoGenerationRequest{Model: "bytedance/seedance-2.0", Input: &schemas.VideoGenerationInput{Prompt: "test"}}
	if err := ValidateUnifiedVideoGenerationRequest(request); err == nil {
		t.Fatal("incomplete unified request accepted")
	}
	if _, err := ToGateVideoGenerationRequest(request); err != nil {
		t.Fatalf("legacy SDK default request rejected: %v", err)
	}
}

func TestGateUnifiedReferencesPreserveWire(t *testing.T) {
	neutral := []schemas.VideoMediaReference{
		{Type: schemas.VideoMediaTypeImage, URL: "https://example.com/first.png", Role: schemas.VideoMediaRoleFirstFrame},
		{Type: schemas.VideoMediaTypeImage, URL: "https://example.com/last.png", Role: schemas.VideoMediaRoleLastFrame},
		{Type: schemas.VideoMediaTypeImage, URL: "https://example.com/reference.png", Role: schemas.VideoMediaRoleReference},
		{Type: schemas.VideoMediaTypeVideo, URL: "https://example.com/reference.mp4", Role: schemas.VideoMediaRoleReferenceVideo},
		{Type: schemas.VideoMediaTypeAudio, URL: "https://example.com/reference.mp3", Role: schemas.VideoMediaRoleReferenceAudio},
	}
	legacy := make([]GateInputReference, len(neutral))
	for index, ref := range neutral {
		legacy[index] = GateInputReference{Type: ref.Type, URL: ref.URL, Role: ref.Role}
	}
	encoded, err := json.Marshal(neutral)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	const expected = `{"model":"bytedance/seedance-2.0","prompt":"test","duration":5,"resolution":"720p","aspect_ratio":"16:9","generate_audio":false,"input_references":[{"type":"image","url":"https://example.com/first.png","role":"first_frame"},{"type":"image","url":"https://example.com/last.png","role":"last_frame"},{"type":"image","url":"https://example.com/reference.png","role":"reference"},{"type":"video","url":"https://example.com/reference.mp4","role":"reference_video"},{"type":"audio","url":"https://example.com/reference.mp3","role":"reference_audio"}],"seed":0}`
	var expectedValue any
	if err := json.Unmarshal([]byte(expected), &expectedValue); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		refs any
	}{{"neutral", neutral}, {"legacy SDK", legacy}, {"decoded JSON", decoded}} {
		t.Run(test.name, func(t *testing.T) {
			request := unifiedGateTestRequest()
			request.Params.ExtraParams[schemas.VideoExtraParamInputReferences] = test.refs
			if err := ValidateUnifiedVideoGenerationRequest(request); err != nil {
				t.Fatal(err)
			}
			mapped, err := ToGateVideoGenerationRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(mapped)
			if err != nil {
				t.Fatal(err)
			}
			var got any
			if err := json.Unmarshal(body, &got); err != nil || !reflect.DeepEqual(got, expectedValue) {
				t.Fatalf("wire changed: %s, %v", body, err)
			}
		})
	}
}

func unifiedGateTestRequest() *schemas.BifrostVideoGenerationRequest {
	return &schemas.BifrostVideoGenerationRequest{
		Model: "bytedance/seedance-2.0", Input: &schemas.VideoGenerationInput{Prompt: "test"},
		Params: &schemas.VideoGenerationParameters{Seconds: schemas.Ptr("5"), Audio: schemas.Ptr(false), Seed: schemas.Ptr(0), ExtraParams: map[string]any{
			schemas.VideoExtraParamResolution: "720p", schemas.VideoExtraParamAspectRatio: "16:9",
			schemas.VideoExtraParamInputReferences: []schemas.VideoMediaReference{{Type: schemas.VideoMediaTypeImage, URL: "https://example.com/first.png", Role: schemas.VideoMediaRoleFirstFrame}},
		}},
	}
}

func BenchmarkGateUnifiedValidation(b *testing.B) {
	request := unifiedGateTestRequest()
	b.ReportAllocs()
	for b.Loop() {
		if err := ValidateUnifiedVideoGenerationRequest(request); err != nil {
			b.Fatal(err)
		}
	}
}

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
