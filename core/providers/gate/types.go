package gate

// Gate wire DTO。这些类型只描述 Gate.AI 线上协议，绝不向 LLMGW 外泄；
// 通用结果走 schemas，计费事实只经 provider-local context accessor 暴露。
//
// 协议来源：https://gate.ai/docs/api-reference/video-generation-api-reference

// GateEnvelope 是 Gate 统一响应信封。成功时 Code 固定为 200（即使 HTTP
// 状态是 202），Data 为对应业务数据；失败时 Msg 携带原因。
type GateEnvelope[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *T     `json:"data"`
}

// GateInputReference 是 Gate 参考素材条目。
type GateInputReference struct {
	Type string `json:"type"` // image / video / audio
	URL  string `json:"url"`  // HTTPS 地址
	Role string `json:"role"` // first_frame / last_frame / reference / reference_video / reference_audio
}

// GateVideoGenerationRequest 是 POST /api/v1/videos 的请求体。
type GateVideoGenerationRequest struct {
	Model           string               `json:"model"`
	Prompt          string               `json:"prompt"`
	Duration        *int                 `json:"duration,omitempty"`
	Resolution      string               `json:"resolution,omitempty"`
	AspectRatio     string               `json:"aspect_ratio,omitempty"`
	GenerateAudio   *bool                `json:"generate_audio,omitempty"`
	Seed            *int                 `json:"seed,omitempty"`
	Size            string               `json:"size,omitempty"`
	InputReferences []GateInputReference `json:"input_references,omitempty"`
	Metadata        map[string]string    `json:"metadata,omitempty"`
}

// GateVideoSubmitData 只建模提交流程实际读取的字段。
type GateVideoSubmitData struct {
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	Model    string `json:"model"`
	Message  string `json:"message"`
	Currency string `json:"currency"`
}

// GateVideoRetrieveData 只建模查询流程实际读取的字段。
// 时间字段为 RFC3339 字符串；金额字段保持原始字符串。
type GateVideoRetrieveData struct {
	JobID         string `json:"job_id"`
	Status        string `json:"status"`
	Model         string `json:"model"`
	Resolution    string `json:"resolution"` // 实际出片档位，映射到标准响应 Size
	Duration      int    `json:"duration"`
	BilledCost    string `json:"billed_cost"`
	BillingStatus string `json:"billing_status"` // pre_deducted / settled
	Currency      string `json:"currency"`       // 当前文档未列出，线上若返回则原样保留
	ExpiresAt     string `json:"expires_at"`
	CreatedAt     string `json:"created_at"`
	CompletedAt   string `json:"completed_at"`
}
