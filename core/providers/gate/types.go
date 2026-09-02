package gate

// Gate wire DTO。这些类型只描述 Gate.AI 线上协议，绝不向 LLMGW 外泄：
// LLMGW 只消费 provider-neutral 的 schemas.BifrostVideoGenerationResponse /
// BifrostVideoDownloadResponse。
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

// GateVideoSubmitData 是提交成功时 envelope.data 的内容。
// 金额字段全部保持原始字符串，绝不转 float64。
type GateVideoSubmitData struct {
	JobID                string `json:"job_id"`
	Status               string `json:"status"`
	Model                string `json:"model"`
	StatusURL            string `json:"status_url"`
	Message              string `json:"message"`
	CurrentBalance       string `json:"current_balance"`
	EstimatedCost        string `json:"estimated_cost"`
	PreDeductAmount      string `json:"pre_deduct_amount"`
	BalanceAfterEstimate string `json:"balance_after_estimate"`
	Currency             string `json:"currency"`
	BillingNotice        string `json:"billing_notice"`
}

// GateVideoRetrieveData 是 GET /api/v1/videos/{job_id} 成功时 envelope.data 的内容。
// 时间字段为 RFC3339 字符串；金额字段保持原始字符串。
type GateVideoRetrieveData struct {
	JobID         string `json:"job_id"`
	Status        string `json:"status"`
	Model         string `json:"model"`
	StatusURL     string `json:"status_url"`
	DownloadURL   string `json:"download_url"`
	Duration      int    `json:"duration"`
	Resolution    string `json:"resolution"`
	AspectRatio   string `json:"aspect_ratio"`
	GenerateAudio bool   `json:"generate_audio"`
	EstimatedCost string `json:"estimated_cost"`
	BilledCost    string `json:"billed_cost"`
	BillingStatus string `json:"billing_status"` // pre_deducted / settled
	ExpiresAt     string `json:"expires_at"`
	// Currency 在 Gate 文档的 retrieve 响应中未列出；若线上实际返回则原样采集，
	// 缺失时由调用方按未知币种 fail-closed 进入 billing_pending。
	Currency    string `json:"currency,omitempty"`
	CreatedAt   string `json:"created_at"`
	CompletedAt string `json:"completed_at"`
}
