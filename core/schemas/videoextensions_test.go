package schemas

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

func TestVideoUsageContextOwnsAllMutableValues(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	usage := videoUsageFixture()
	// 新增可变字段必须进入夹具，避免只证明部分字段没有指针别名。
	value := reflect.ValueOf(usage).Elem()
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if (field.Kind() == reflect.Pointer || field.Kind() == reflect.Slice || field.Kind() == reflect.Map) && field.IsNil() {
			t.Fatalf("fixture does not cover mutable field %s", value.Type().Field(index).Name)
		}
	}
	metadata := VideoUsageMetadata{Source: VideoUsageSourceModelRule, Resolution: "720p", GenerateAudio: Ptr(false)}
	if !SetVideoUsageOnContext(ctx, "task", usage, metadata) {
		t.Fatal("valid snapshot rejected")
	}
	mutateVideoUsageValues(reflect.ValueOf(usage))
	*metadata.GenerateAudio = true
	id, got, facts, ok := VideoUsageFromContext(ctx)
	if !ok || id != "task" || !reflect.DeepEqual(got, videoUsageFixture()) || facts.GenerateAudio == nil || *facts.GenerateAudio {
		t.Fatalf("writer changed captured snapshot: %q %#v %#v %v", id, got, facts, ok)
	}
	mutateVideoUsageValues(reflect.ValueOf(got))
	*facts.GenerateAudio = true
	_, again, againFacts, ok := VideoUsageFromContext(ctx)
	if !ok || !reflect.DeepEqual(again, videoUsageFixture()) || *againFacts.GenerateAudio {
		t.Fatal("reader changed captured snapshot")
	}
}

func TestVideoUsageContextPresenceAndClearing(t *testing.T) {
	if SetVideoUsageOnContext(nil, "task", nil, VideoUsageMetadata{}) {
		t.Fatal("nil context accepted")
	}
	ClearVideoUsageOnContext(nil)
	if _, _, _, ok := VideoUsageFromContext(nil); ok {
		t.Fatal("nil context has a result")
	}
	ctx := NewBifrostContext(nil, NoDeadline)
	if SetVideoUsageOnContext(ctx, "", nil, VideoUsageMetadata{}) {
		t.Fatal("empty task identity accepted")
	}
	if _, _, _, ok := VideoUsageFromContext(ctx); ok {
		t.Fatal("empty task identity created a result")
	}
	for _, test := range []struct {
		name  string
		usage *BifrostLLMUsage
	}{
		{name: "missing usage"},
		{name: "explicit zero", usage: &BifrostLLMUsage{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ClearVideoUsageOnContext(ctx)
			if _, _, _, ok := VideoUsageFromContext(ctx); ok {
				t.Fatal("previous query result survived clear")
			}
			if !SetVideoUsageOnContext(ctx, test.name, test.usage, VideoUsageMetadata{GenerateAudio: Ptr(false)}) {
				t.Fatal("terminal facts rejected")
			}
			id, usage, metadata, ok := VideoUsageFromContext(ctx)
			if !ok || id != test.name || (usage == nil) != (test.usage == nil) || metadata.GenerateAudio == nil || *metadata.GenerateAudio {
				t.Fatalf("presence changed: %q %#v %#v %v", id, usage, metadata, ok)
			}
		})
	}
	ClearVideoUsageOnContext(ctx)
	if _, _, _, ok := VideoUsageFromContext(ctx); ok {
		t.Fatal("last result survived clear")
	}
}

func TestVideoUsageContextConcurrentIsolation(t *testing.T) {
	var workers sync.WaitGroup
	for n := 0; n < 24; n++ {
		workers.Go(func() {
			ctx := NewBifrostContext(nil, NoDeadline)
			SetVideoUsageOnContext(ctx, "task", &BifrostLLMUsage{CompletionTokens: n}, VideoUsageMetadata{GenerateAudio: Ptr(n%2 == 0)})
			_, got, metadata, ok := VideoUsageFromContext(ctx)
			if !ok || got.CompletionTokens != n || *metadata.GenerateAudio != (n%2 == 0) {
				t.Errorf("result leaked across contexts: %#v %#v", got, metadata)
			}
		})
	}
	workers.Wait()
}

func TestVideoUsageClearShadowsParentResult(t *testing.T) {
	parent := NewBifrostContext(nil, NoDeadline)
	SetVideoUsageOnContext(parent, "parent-task", &BifrostLLMUsage{CompletionTokens: 8}, VideoUsageMetadata{})
	for _, test := range []struct {
		name   string
		parent context.Context
	}{
		{name: "direct parent", parent: parent},
		{name: "wrapped parent", parent: context.WithValue(parent, struct{ label string }{"unrelated"}, true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			child := NewBifrostContext(test.parent, NoDeadline)
			ClearVideoUsageOnContext(child)
			if id, _, _, ok := VideoUsageFromContext(child); ok {
				t.Fatalf("cleared child inherited task %q", id)
			}
			if id, _, _, ok := VideoUsageFromContext(parent); !ok || id != "parent-task" {
				t.Fatal("clearing child changed parent result")
			}
			SetVideoUsageOnContext(child, "child-task", nil, VideoUsageMetadata{})
			if id, _, _, ok := VideoUsageFromContext(child); !ok || id != "child-task" {
				t.Fatal("child could not record its own task")
			}
		})
	}
}

func TestVideoExtensionsJSONContract(t *testing.T) {
	reference, err := json.Marshal(VideoMediaReference{Type: VideoMediaTypeImage, URL: "https://example.com/frame.png", Role: VideoMediaRoleFirstFrame})
	if err != nil || string(reference) != `{"type":"image","url":"https://example.com/frame.png","role":"first_frame"}` {
		t.Fatalf("reference contract changed: %s, %v", reference, err)
	}
	metadata, err := json.Marshal(VideoUsageMetadata{Source: VideoUsageSourceProviderReported, GenerateAudio: Ptr(false)})
	if err != nil || string(metadata) != `{"source":"provider_reported","generate_audio":false}` {
		t.Fatalf("metadata contract changed: %s, %v", metadata, err)
	}
}

func videoUsageFixture() *BifrostLLMUsage {
	return &BifrostLLMUsage{
		PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
		PromptTokensDetails:     &ChatPromptTokensDetails{TextTokens: 1, CachedWriteTokenDetails: &ChatCachedWriteTokenDetails{CachedWriteTokens5m: 1, CachedWriteTokens1h: 2}},
		CompletionTokensDetails: &ChatCompletionTokensDetails{TextTokens: 2, CitationTokens: Ptr(1), NumSearchQueries: Ptr(2), ImageTokens: Ptr(3)},
		AudioSeconds:            Ptr(1.5), SearchUnits: Ptr(2), ToolUsage: &ToolUsage{WebSearch: &WebSearchToolUsage{NumRequests: 2}},
		Cost: &BifrostCost{InputCost: 1, InputCostDetails: &InputCostDetails{TextCost: 1}, OutputCost: 2,
			OutputCostDetails: &OutputCostDetails{TextCost: 2}, AdditionalCost: 3, AdditionalCostDetails: &AdditionalCostDetails{GuardrailCost: 3}, TotalCost: 6},
		CostInUsdTicks: Ptr(int64(6)), StateTokens: Ptr(4), StateTokensDropped: Ptr(5), Truncated: Ptr(false), TruncatedQuestions: []string{"original"},
		Speed: Ptr("fast"), InferenceGeo: Ptr("eu"), ServerSideFallbackModel: Ptr("model"),
	}
}

// 逐字段改写测试对象，证明输入和返回值的全部可变后代均与保存的快照分离。
func mutateVideoUsageValues(value reflect.Value) {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !value.IsNil() {
			mutateVideoUsageValues(value.Elem())
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			mutateVideoUsageValues(value.Field(index))
		}
	case reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			mutateVideoUsageValues(value.Index(index))
		}
	case reflect.String:
		value.SetString("mutated")
	case reflect.Bool:
		value.SetBool(!value.Bool())
	case reflect.Int, reflect.Int64:
		value.SetInt(999)
	case reflect.Float64:
		value.SetFloat(999)
	}
}

func BenchmarkVideoUsageContextMissing(b *testing.B) {
	ctx := NewBifrostContext(nil, NoDeadline)
	b.ReportAllocs()
	for b.Loop() {
		VideoUsageFromContext(ctx)
	}
}
