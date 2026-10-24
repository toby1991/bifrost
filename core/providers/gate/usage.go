package gate

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// VideoUsageMetadata 保留已发布 SDK 的类型名称与 JSON 形状。
// Deprecated: use schemas.VideoUsageMetadata.
type VideoUsageMetadata = schemas.VideoUsageMetadata

// VideoUsageFromContext 保留已发布 SDK 的 accessor。
// Deprecated: use schemas.VideoUsageFromContext.
func VideoUsageFromContext(ctx *schemas.BifrostContext) (taskID string, usage *schemas.BifrostLLMUsage, metadata VideoUsageMetadata, ok bool) {
	return schemas.VideoUsageFromContext(ctx)
}

func clearVideoUsage(ctx *schemas.BifrostContext) {
	schemas.ClearVideoUsageOnContext(ctx)
}

func setVideoUsage(ctx *schemas.BifrostContext, taskID string, usage *schemas.BifrostLLMUsage, metadata VideoUsageMetadata) {
	schemas.SetVideoUsageOnContext(ctx, taskID, usage, metadata)
}

func normalizeVideoUsage(data *GateVideoRetrieveData) (*schemas.BifrostLLMUsage, VideoUsageMetadata) {
	metadata := VideoUsageMetadata{
		Resolution: data.Resolution, AspectRatio: data.AspectRatio,
		Duration: data.Duration, GenerateAudio: data.GenerateAudio,
	}
	// 只接受明确的输出 Token 字段；total_tokens 可能含输入 Token，不能当输出。
	if tokens, present, valid := reportedOutputTokens(data.Usage); present {
		if !valid {
			return nil, metadata
		}
		metadata.Source = schemas.VideoUsageSourceProviderReported
		metadata.RawOutputTokens = strconv.Itoa(tokens)
		metadata.RoundedOutputTokens = tokens
		return &schemas.BifrostLLMUsage{CompletionTokens: tokens}, metadata
	}
	// 这是指定模型的计量规则，不是跨模型的帧数/媒体探测算法。
	if data.Model != "bytedance/seedance-2.0" || data.Duration <= 0 || int64(data.Duration) > (math.MaxInt64-1)/24 {
		return nil, metadata
	}
	dimensions, ok := seedance20Dimensions[data.Resolution][data.AspectRatio]
	if !ok {
		return nil, metadata
	}
	frames := int64(data.Duration)*24 + 1
	numerator := new(big.Int).Mul(big.NewInt(int64(dimensions[0]*dimensions[1])), big.NewInt(frames))
	denominator := big.NewInt(1024)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || int64(int(quotient.Int64())) != quotient.Int64() {
		return nil, metadata
	}
	metadata.Source = schemas.VideoUsageSourceModelRule
	metadata.RuleVersion = "seedance-2.0-24fps-plus-one-v1"
	metadata.Width, metadata.Height = dimensions[0], dimensions[1]
	metadata.Frames = frames
	// 分母 1024 最多产生 10 位有限小数，字符串保留精确证据而非 float64 近似。
	raw := new(big.Rat).SetFrac(numerator, denominator).FloatString(10)
	metadata.RawOutputTokens = strings.TrimRight(strings.TrimRight(raw, "0"), ".")
	metadata.RoundedOutputTokens = int(quotient.Int64())
	return &schemas.BifrostLLMUsage{CompletionTokens: metadata.RoundedOutputTokens}, metadata
}

func reportedOutputTokens(raw json.RawMessage) (tokens int, present, valid bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false, false
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return 0, true, false
	}
	for _, key := range []string{"completion_tokens", "output_tokens"} {
		value, ok := usage[key]
		if !ok {
			continue
		}
		var count int
		if string(value) == "null" || json.Unmarshal(value, &count) != nil || count < 0 {
			return 0, true, false
		}
		if present && tokens != count {
			return 0, true, false
		}
		tokens, present = count, true
	}
	return tokens, present, present
}

// Seedance 2.0 官方规格表；不能用 normalizeGateVideoSize 的代表性计费尺寸
// 推导 Token。未列出的档位/比例没有推导规则，未来模型必须独立实现。
var seedance20Dimensions = map[string]map[string][2]int{
	"480p":  {"16:9": {864, 496}, "4:3": {752, 560}, "1:1": {640, 640}, "3:4": {560, 752}, "9:16": {496, 864}, "21:9": {992, 432}},
	"720p":  {"16:9": {1280, 720}, "4:3": {1112, 834}, "1:1": {960, 960}, "3:4": {834, 1112}, "9:16": {720, 1280}, "21:9": {1470, 630}},
	"1080p": {"16:9": {1920, 1080}, "4:3": {1664, 1248}, "1:1": {1440, 1440}, "3:4": {1248, 1664}, "9:16": {1080, 1920}, "21:9": {2206, 946}},
	"4k":    {"16:9": {3840, 2160}, "4:3": {3326, 2494}, "1:1": {2880, 2880}, "3:4": {2494, 3326}, "9:16": {2160, 3840}, "21:9": {4398, 1886}},
}
