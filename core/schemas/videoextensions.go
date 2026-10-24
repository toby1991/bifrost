package schemas

// VideoMediaReference 是通过 ExtraParams 传递的中性素材；具体厂商的 wire DTO 由 adapter 构造。
type VideoMediaReference struct {
	Type string `json:"type"`
	URL  string `json:"url"`
	Role string `json:"role"`
}

const (
	VideoExtraParamInputReferences = "input_references"
	VideoExtraParamResolution      = "resolution"
	VideoExtraParamAspectRatio     = "aspect_ratio"

	VideoMediaTypeImage = "image"
	VideoMediaTypeVideo = "video"
	VideoMediaTypeAudio = "audio"

	VideoMediaRoleFirstFrame     = "first_frame"
	VideoMediaRoleLastFrame      = "last_frame"
	VideoMediaRoleReference      = "reference"
	VideoMediaRoleReferenceVideo = "reference_video"
	VideoMediaRoleReferenceAudio = "reference_audio"

	VideoUsageSourceProviderReported = "provider_reported"
	VideoUsageSourceModelRule        = "model_rule"
)

// VideoUsageMetadata 保存终态实际规格和计量来源；不得用请求规格补造缺失事实。
// 真实像素与计价代表性 Size 分开保留，JSON 形状兼容既有视频证据。
type VideoUsageMetadata struct {
	Source              string `json:"source,omitempty"`
	RuleVersion         string `json:"rule_version,omitempty"`
	Resolution          string `json:"resolution,omitempty"`
	AspectRatio         string `json:"aspect_ratio,omitempty"`
	Duration            int    `json:"duration,omitempty"`
	GenerateAudio       *bool  `json:"generate_audio,omitempty"`
	Width               int    `json:"width,omitempty"`
	Height              int    `json:"height,omitempty"`
	Frames              int64  `json:"frames,omitempty"`
	RawOutputTokens     string `json:"raw_output_tokens,omitempty"`
	RoundedOutputTokens int    `json:"rounded_output_tokens,omitempty"`
}

type videoUsageContextKey struct{}

type videoUsageResult struct {
	taskID   string
	usage    *BifrostLLMUsage
	metadata VideoUsageMetadata
}

// SetVideoUsageOnContext 保存 adapter 归一后的终态事实，复制所有可变输入。
// usage=nil 表示未取得 Token，不能与明确的零用量合并。每次查询开始应先清理旧值。
func SetVideoUsageOnContext(ctx *BifrostContext, taskID string, usage *BifrostLLMUsage, metadata VideoUsageMetadata) bool {
	if ctx == nil || taskID == "" {
		return false
	}
	ctx.SetValue(videoUsageContextKey{}, videoUsageResult{
		taskID: taskID, usage: cloneVideoUsage(usage), metadata: cloneVideoUsageMetadata(metadata),
	})
	return true
}

// VideoUsageFromContext 返回独立的终态快照。ok 仅表示存在任务事实，不保证足以按当前价表计费。
func VideoUsageFromContext(ctx *BifrostContext) (taskID string, usage *BifrostLLMUsage, metadata VideoUsageMetadata, ok bool) {
	if ctx == nil {
		return "", nil, VideoUsageMetadata{}, false
	}
	result, ok := ctx.Value(videoUsageContextKey{}).(videoUsageResult)
	if !ok || result.taskID == "" {
		return "", nil, VideoUsageMetadata{}, false
	}
	return result.taskID, cloneVideoUsage(result.usage), cloneVideoUsageMetadata(result.metadata), true
}

// ClearVideoUsageOnContext 在新一轮查询前清除旧任务事实，避免复用 context 时串用计量结果。
func ClearVideoUsageOnContext(ctx *BifrostContext) {
	if ctx != nil {
		// 全新的子 context 也必须写入空值，屏蔽父链上的旧任务事实。
		ctx.SetValue(videoUsageContextKey{}, nil)
	}
}

func cloneVideoUsageMetadata(metadata VideoUsageMetadata) VideoUsageMetadata {
	if metadata.GenerateAudio != nil {
		value := *metadata.GenerateAudio
		metadata.GenerateAudio = &value
	}
	return metadata
}

func cloneVideoUsage(usage *BifrostLLMUsage) *BifrostLLMUsage {
	copy := usage.DeepCopy()
	if copy != nil {
		// 既有 DeepCopy 尚未复制 AudioSeconds；视频快照在此补齐，不改变其它调用方。
		copy.AudioSeconds = cloneFloat64(usage.AudioSeconds)
	}
	return copy
}
