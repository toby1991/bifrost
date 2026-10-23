package gate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestGateInputReferences(t *testing.T) {
	references := []GateInputReference{
		{Type: "image", URL: "https://cdn.example.com/first.png", Role: "first_frame"},
		{Type: "image", URL: "https://cdn.example.com/last.png", Role: "last_frame"},
		{Type: "image", URL: "https://cdn.example.com/reference.png", Role: "reference"},
		{Type: "video", URL: "https://cdn.example.com/reference.mp4", Role: "reference_video"},
		{Type: "audio", URL: "https://cdn.example.com/reference.mp3", Role: "reference_audio"},
	}
	tests := []struct {
		name  string
		value any
	}{
		{"typed references", references},
		{"decoded JSON", []any{
			map[string]any{"type": "image", "url": references[0].URL, "role": "first_frame"},
			map[string]any{"type": "image", "url": references[1].URL, "role": "last_frame"},
			map[string]any{"type": "image", "url": references[2].URL, "role": "reference"},
			map[string]any{"type": "video", "url": references[3].URL, "role": "reference_video"},
			map[string]any{"type": "audio", "url": references[4].URL, "role": "reference_audio"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := &schemas.BifrostVideoGenerationRequest{
				Model: "bytedance/seedance-2.0", Input: &schemas.VideoGenerationInput{Prompt: "test"},
				Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{ExtraParamInputReferences: tt.value}},
			}
			mapped, err := ToGateVideoGenerationRequest(request)
			if err != nil || !reflect.DeepEqual(mapped.InputReferences, references) {
				t.Fatalf("mapping = %#v, %v", mapped, err)
			}
			mapped.InputReferences[0].URL = "https://changed.example.com"
			if references[0].URL != "https://cdn.example.com/first.png" {
				t.Fatal("mapping mutated input references")
			}
		})
	}
}

func TestGateInvalidReferencesDoNotDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "must not dispatch", http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	ref := func(kind, role, url string) []GateInputReference {
		return []GateInputReference{{Type: kind, Role: role, URL: url}}
	}
	tests := []struct {
		name   string
		value  any
		legacy *string
	}{
		{"null", nil, nil},
		{"object", map[string]string{}, nil},
		{"unknown field", []map[string]any{{"type": "image", "role": "first_frame", "url": "https://cdn.example.com/a", "unknown": true}}, nil},
		{"wrong type", ref("image", "reference_video", "https://cdn.example.com/a"), nil},
		{"unknown type", ref("document", "reference", "https://cdn.example.com/a"), nil},
		{"unknown role", ref("audio", "reference", "https://cdn.example.com/a"), nil},
		{"http", ref("image", "first_frame", "http://cdn.example.com/a"), nil},
		{"relative", ref("image", "first_frame", "/image.png"), nil},
		{"userinfo", ref("image", "first_frame", "https://secret@cdn.example.com/a"), nil},
		{"missing host", ref("image", "first_frame", "https:/image.png"), nil},
		{"empty hostname with port", ref("image", "first_frame", "https://:443/image.png"), nil},
		{"duplicate first", append(ref("image", "first_frame", "https://cdn.example.com/a"), ref("image", "first_frame", "https://cdn.example.com/b")...), nil},
		{"duplicate last", append(ref("image", "last_frame", "https://cdn.example.com/a"), ref("image", "last_frame", "https://cdn.example.com/b")...), nil},
		{"legacy conflict", ref("image", "reference", "https://cdn.example.com/a"), schemas.Ptr("https://cdn.example.com/b")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.VideoGeneration(testCtx(), testKey("local-test"), &schemas.BifrostVideoGenerationRequest{
				Model: "bytedance/seedance-2.0", Input: &schemas.VideoGenerationInput{Prompt: "test", InputReference: tt.legacy},
				Params: &schemas.VideoGenerationParameters{ExtraParams: map[string]any{ExtraParamInputReferences: tt.value}},
			})
			if err == nil {
				t.Fatal("invalid reference unexpectedly accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid references dispatched %d requests", calls.Load())
	}
}

func TestGateReferencesWireRetainsFalseZero(t *testing.T) {
	references := []GateInputReference{{Type: "video", URL: "https://cdn.example.com/a.mp4", Role: "reference_video"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if string(body["generate_audio"]) != "false" || string(body["seed"]) != "0" {
			t.Errorf("lost explicit zero values: %v", body)
		}
		var got []GateInputReference
		if err := json.Unmarshal(body[ExtraParamInputReferences], &got); err != nil || !reflect.DeepEqual(got, references) {
			t.Errorf("wire references = %#v, %v", got, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"code":200,"data":{"job_id":"video_reference","status":"pending","model":"bytedance/seedance-2.0","currency":"USD"}}`)
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)
	response, err := provider.VideoGeneration(testCtx(), testKey("local-test"), &schemas.BifrostVideoGenerationRequest{
		Model: "bytedance/seedance-2.0", Input: &schemas.VideoGenerationInput{Prompt: "test"},
		Params: &schemas.VideoGenerationParameters{Audio: schemas.Ptr(false), Seed: schemas.Ptr(0), ExtraParams: map[string]any{ExtraParamInputReferences: references}},
	})
	if err != nil || response == nil || !strings.Contains(response.ID, "video_reference") {
		t.Fatalf("generation = %#v, %v", response, err)
	}
}
