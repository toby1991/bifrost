package datasheet

import (
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const chatPricingMode = "chat"

// PricingFieldSpec describes one accepted pricing field for a normalized mode.
type PricingFieldSpec struct {
	Key      string
	Required bool
}

// PricingSpec is the immutable public schema for one normalized pricing mode.
type PricingSpec struct {
	NormalizedRequestType string
	Fields                []PricingFieldSpec
}

// ChatCostContext carries only response facts that select a served chat rate.
type ChatCostContext struct {
	ServiceTier  *schemas.BifrostServiceTier
	Speed        *string
	InferenceGeo *string
}

var chatPricingFields = []PricingFieldSpec{
	{Key: "input_cost_per_token", Required: true},
	{Key: "output_cost_per_token", Required: true},
	{Key: "input_cost_per_token_priority"},
	{Key: "output_cost_per_token_priority"},
	{Key: "input_cost_per_token_flex"},
	{Key: "output_cost_per_token_flex"},
	{Key: "input_cost_per_token_fast"},
	{Key: "output_cost_per_token_fast"},
	{Key: "input_cost_per_token_above_128k_tokens"},
	{Key: "output_cost_per_token_above_128k_tokens"},
	{Key: "input_cost_per_token_above_200k_tokens"},
	{Key: "input_cost_per_token_above_200k_tokens_priority"},
	{Key: "output_cost_per_token_above_200k_tokens"},
	{Key: "output_cost_per_token_above_200k_tokens_priority"},
	{Key: "input_cost_per_token_above_272k_tokens"},
	{Key: "input_cost_per_token_above_272k_tokens_priority"},
	{Key: "input_cost_per_token_flex_above_272k_tokens"},
	{Key: "output_cost_per_token_above_272k_tokens"},
	{Key: "output_cost_per_token_above_272k_tokens_priority"},
	{Key: "output_cost_per_token_flex_above_272k_tokens"},
	{Key: "cache_creation_input_token_cost"},
	{Key: "cache_read_input_token_cost"},
	{Key: "cache_creation_input_token_cost_above_200k_tokens"},
	{Key: "cache_read_input_token_cost_above_200k_tokens"},
	{Key: "cache_read_input_token_cost_above_200k_tokens_priority"},
	{Key: "cache_creation_input_token_cost_above_1hr"},
	{Key: "cache_creation_input_token_cost_above_1hr_above_200k_tokens"},
	{Key: "cache_read_input_token_cost_priority"},
	{Key: "cache_read_input_token_cost_flex"},
	{Key: "cache_read_input_token_cost_above_272k_tokens"},
	{Key: "cache_read_input_token_cost_above_272k_tokens_priority"},
	{Key: "cache_read_input_token_cost_flex_above_272k_tokens"},
	{Key: "cache_creation_input_token_cost_above_272k_tokens"},
	{Key: "cache_creation_input_token_cost_flex"},
	{Key: "cache_creation_input_token_cost_flex_above_272k_tokens"},
	{Key: "cache_creation_input_token_cost_priority"},
	{Key: "cache_creation_input_token_cost_fast"},
	{Key: "cache_creation_input_token_cost_above_1hr_fast"},
	{Key: "cache_read_input_token_cost_fast"},
	{Key: "input_cost_per_audio_token"},
	{Key: "output_cost_per_audio_token"},
	{Key: "search_context_cost_per_query"},
	{Key: "inference_geo_us_multiplier"},
	// Keep new fields appended so existing consumers see a stable prefix.
	{Key: "input_cost_per_token_ultrafast"},
	{Key: "output_cost_per_token_ultrafast"},
	{Key: "cache_read_input_token_cost_ultrafast"},
	{Key: "cache_creation_input_token_cost_ultrafast"},
	{Key: "cost_per_request"},
}

// NormalizeRequestType maps a raw Core request type to the pricing mode used
// by Framework. Unknown request types are rejected explicitly.
func NormalizeRequestType(raw schemas.RequestType) (string, bool) {
	mode := normalizeRequestType(raw)
	if mode == "unknown" {
		return "", false
	}
	return mode, true
}

// ChatPricingSpec returns the complete pricing schema consumed by the chat
// calculator. The returned slice never aliases Framework's registry storage.
func ChatPricingSpec() PricingSpec {
	fields := make([]PricingFieldSpec, len(chatPricingFields))
	copy(fields, chatPricingFields)
	return PricingSpec{NormalizedRequestType: chatPricingMode, Fields: fields}
}

// CalculateChatUsageCostWithPricing computes chat cost from an explicit,
// immutable pricing snapshot and usage evidence. It deliberately performs no
// catalog lookup and ignores any provider-populated usage.Cost value.
func CalculateChatUsageCostWithPricing(
	raw schemas.RequestType,
	usage *schemas.BifrostLLMUsage,
	ctx ChatCostContext,
	pricing Options,
) (float64, error) {
	// 纯 Chat 计价只接受这两种原始请求，避免归一化后的批量请求误用同步费率。
	if raw != schemas.ChatCompletionRequest && raw != schemas.ChatCompletionStreamRequest {
		return 0, fmt.Errorf("request type %q is not supported by chat pricing", raw)
	}
	if usage == nil {
		return 0, fmt.Errorf("chat usage is required")
	}
	if err := validateChatPricing(pricing); err != nil {
		return 0, err
	}

	row := convertEntryToTablePricing("", Entry{Mode: chatPricingMode, Options: pricing})
	breakdown := computeTextCost(&row, usage, tierFromResponse(ctx.ServiceTier, ctx.Speed, ctx.InferenceGeo))
	cost := 0.0
	if breakdown != nil {
		cost = breakdown.TotalCost
	}
	// computeTextCost only prices usage categories. The public pure API mirrors
	// Store.computeCostFromInput by applying the flat request fee exactly once.
	if row.CostPerRequest != nil {
		cost += *row.CostPerRequest
	}
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return 0, fmt.Errorf("chat cost must be finite and non-negative")
	}
	return cost, nil
}

func validateChatPricing(pricing Options) error {
	allowed := make(map[string]bool, len(chatPricingFields))
	for _, field := range chatPricingFields {
		allowed[field.Key] = field.Required
	}

	value := reflect.ValueOf(pricing)
	typeOfOptions := value.Type()
	for i := 0; i < value.NumField(); i++ {
		fieldValue := value.Field(i)
		if fieldValue.Kind() != reflect.Pointer || fieldValue.IsNil() {
			continue
		}

		jsonName := strings.SplitN(typeOfOptions.Field(i).Tag.Get("json"), ",", 2)[0]
		if _, ok := allowed[jsonName]; !ok {
			return fmt.Errorf("pricing field %q is not supported by chat pricing", jsonName)
		}

		rate := fieldValue.Elem().Float()
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return fmt.Errorf("pricing field %q must be finite and non-negative", jsonName)
		}
		if jsonName == "inference_geo_us_multiplier" && rate == 0 {
			return fmt.Errorf("pricing field %q must be greater than zero", jsonName)
		}
		delete(allowed, jsonName)
	}

	for _, field := range chatPricingFields {
		if required, missing := allowed[field.Key]; missing && required {
			return fmt.Errorf("required chat pricing field %q is missing", field.Key)
		}
	}
	return nil
}
