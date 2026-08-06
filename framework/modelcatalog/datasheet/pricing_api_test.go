package datasheet

import (
	"math"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ func(
	schemas.RequestType,
	*schemas.BifrostLLMUsage,
	ChatCostContext,
	Options,
) (float64, error) = CalculateChatUsageCostWithPricing

func TestNormalizeRequestTypePublicAPI(t *testing.T) {
	tests := []struct {
		raw  schemas.RequestType
		mode string
	}{
		{schemas.TextCompletionRequest, "completion"},
		{schemas.TextCompletionStreamRequest, "completion"},
		{schemas.ChatCompletionRequest, "chat"},
		{schemas.ChatCompletionStreamRequest, "chat"},
		{schemas.ResponsesRequest, "responses"},
		{schemas.ResponsesStreamRequest, "responses"},
		{schemas.WebSocketResponsesRequest, "responses"},
		{schemas.RealtimeRequest, "responses"},
		{schemas.CompactionRequest, "responses"},
		{schemas.EmbeddingRequest, "embedding"},
		{schemas.RerankRequest, "rerank"},
		{schemas.SpeechRequest, "audio_speech"},
		{schemas.SpeechStreamRequest, "audio_speech"},
		{schemas.TranscriptionRequest, "audio_transcription"},
		{schemas.TranscriptionStreamRequest, "audio_transcription"},
		{schemas.ImageGenerationRequest, "image_generation"},
		{schemas.ImageGenerationStreamRequest, "image_generation"},
		{schemas.ImageVariationRequest, "image_generation"},
		{schemas.ImageEditRequest, "image_edit"},
		{schemas.ImageEditStreamRequest, "image_edit"},
		{schemas.VideoGenerationRequest, "video_generation"},
		{schemas.VideoRemixRequest, "video_generation"},
		{schemas.OCRRequest, "ocr"},
		{schemas.ContainerCreateRequest, "container_create"},
	}
	for _, test := range tests {
		mode, ok := NormalizeRequestType(test.raw)
		require.True(t, ok, test.raw)
		assert.Equal(t, test.mode, mode, test.raw)
	}

	for _, raw := range []schemas.RequestType{
		schemas.UnknownRequest,
		schemas.ListModelsRequest,
		"",
		"chat",
		"future_request",
	} {
		mode, ok := NormalizeRequestType(raw)
		assert.False(t, ok, raw)
		assert.Empty(t, mode, raw)
	}
}

func TestChatPricingSpecFixture(t *testing.T) {
	expected := []string{
		"input_cost_per_token",
		"output_cost_per_token",
		"input_cost_per_token_priority",
		"output_cost_per_token_priority",
		"input_cost_per_token_flex",
		"output_cost_per_token_flex",
		"input_cost_per_token_fast",
		"output_cost_per_token_fast",
		"input_cost_per_token_above_128k_tokens",
		"output_cost_per_token_above_128k_tokens",
		"input_cost_per_token_above_200k_tokens",
		"input_cost_per_token_above_200k_tokens_priority",
		"output_cost_per_token_above_200k_tokens",
		"output_cost_per_token_above_200k_tokens_priority",
		"input_cost_per_token_above_272k_tokens",
		"input_cost_per_token_above_272k_tokens_priority",
		"input_cost_per_token_flex_above_272k_tokens",
		"output_cost_per_token_above_272k_tokens",
		"output_cost_per_token_above_272k_tokens_priority",
		"output_cost_per_token_flex_above_272k_tokens",
		"cache_creation_input_token_cost",
		"cache_read_input_token_cost",
		"cache_creation_input_token_cost_above_200k_tokens",
		"cache_read_input_token_cost_above_200k_tokens",
		"cache_read_input_token_cost_above_200k_tokens_priority",
		"cache_creation_input_token_cost_above_1hr",
		"cache_creation_input_token_cost_above_1hr_above_200k_tokens",
		"cache_read_input_token_cost_priority",
		"cache_read_input_token_cost_flex",
		"cache_read_input_token_cost_above_272k_tokens",
		"cache_read_input_token_cost_above_272k_tokens_priority",
		"cache_read_input_token_cost_flex_above_272k_tokens",
		"cache_creation_input_token_cost_above_272k_tokens",
		"cache_creation_input_token_cost_flex",
		"cache_creation_input_token_cost_flex_above_272k_tokens",
		"cache_creation_input_token_cost_priority",
		"cache_creation_input_token_cost_fast",
		"cache_creation_input_token_cost_above_1hr_fast",
		"cache_read_input_token_cost_fast",
		"input_cost_per_audio_token",
		"output_cost_per_audio_token",
		"search_context_cost_per_query",
		"inference_geo_us_multiplier",
	}

	spec := ChatPricingSpec()
	assert.Equal(t, "chat", spec.NormalizedRequestType)
	require.Len(t, spec.Fields, len(expected))
	for i, field := range spec.Fields {
		assert.Equal(t, expected[i], field.Key)
		assert.Equal(t, i < 2, field.Required)
	}

	spec.Fields[0].Required = false
	assert.True(t, ChatPricingSpec().Fields[0].Required)
}

func TestCalculateChatUsageCostWithPricingFixture(t *testing.T) {
	pricing := Options{
		InputCostPerToken:           bifrost.Ptr(0.00001),
		OutputCostPerToken:          bifrost.Ptr(0.00005),
		CacheReadInputTokenCost:     bifrost.Ptr(0.000001),
		CacheCreationInputTokenCost: bifrost.Ptr(0.0000125),
		SearchContextCostPerQuery:   bifrost.Ptr(0.01),
		InferenceGeoUSMultiplier:    bifrost.Ptr(1.1),
	}
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 100,
		TotalTokens:      1100,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  200,
			CachedWriteTokens: 300,
		},
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			NumSearchQueries: bifrost.Ptr(2),
		},
		Cost: &schemas.BifrostCost{TotalCost: 999},
	}
	ctx := ChatCostContext{InferenceGeo: bifrost.Ptr("US")}

	tokenCost := 500*0.00001 + 200*0.000001 + 300*0.0000125 + 100*0.00005
	expected := tokenCost*1.1 + 2*0.01
	for _, raw := range []schemas.RequestType{schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest} {
		got, err := CalculateChatUsageCostWithPricing(raw, usage, ctx, pricing)
		require.NoError(t, err)
		assert.InDelta(t, expected, got, 1e-12)
	}
}

func TestCalculateChatUsageCostWithPricingServedTier(t *testing.T) {
	pricing := Options{
		InputCostPerToken:          bifrost.Ptr(0.000005),
		OutputCostPerToken:         bifrost.Ptr(0.00003),
		InputCostPerTokenPriority:  bifrost.Ptr(0.00001),
		OutputCostPerTokenPriority: bifrost.Ptr(0.00006),
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 100}
	tier := schemas.BifrostServiceTierPriority

	got, err := CalculateChatUsageCostWithPricing(
		schemas.ChatCompletionRequest,
		usage,
		ChatCostContext{ServiceTier: &tier},
		pricing,
	)
	require.NoError(t, err)
	assert.InDelta(t, 1000*0.00001+100*0.00006, got, 1e-12)
}

func TestCalculateChatUsageCostWithPricingRejectsInvalidInput(t *testing.T) {
	valid := Options{
		InputCostPerToken:  bifrost.Ptr(0.000005),
		OutputCostPerToken: bifrost.Ptr(0.00003),
	}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 1}

	_, err := CalculateChatUsageCostWithPricing(schemas.EmbeddingRequest, usage, ChatCostContext{}, valid)
	require.Error(t, err)
	_, err = CalculateChatUsageCostWithPricing(schemas.ChatCompletionRequest, nil, ChatCostContext{}, valid)
	require.Error(t, err)

	missingRequired := valid
	missingRequired.OutputCostPerToken = nil
	_, err = CalculateChatUsageCostWithPricing(schemas.ChatCompletionRequest, usage, ChatCostContext{}, missingRequired)
	require.Error(t, err)

	unsupported := valid
	unsupported.InputCostPerImage = bifrost.Ptr(1.0)
	_, err = CalculateChatUsageCostWithPricing(schemas.ChatCompletionRequest, usage, ChatCostContext{}, unsupported)
	require.Error(t, err)

	notFinite := valid
	notFinite.InputCostPerToken = bifrost.Ptr(math.NaN())
	_, err = CalculateChatUsageCostWithPricing(schemas.ChatCompletionRequest, usage, ChatCostContext{}, notFinite)
	require.Error(t, err)
}
