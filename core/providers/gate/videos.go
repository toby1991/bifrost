package gate

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// Gate 固定 API 路径。
const gateVideosPath = "/api/v1/videos"

// Gate 提交成功信封 code（即使 HTTP 状态为 202，envelope.code 也是 200）。
const gateEnvelopeCodeSuccess = 200

// ExtraParams 白名单：resolution/aspect_ratio/metadata 之外一律拒绝，
// 防止任意 provider payload（如 webhook_url）穿透到上游。
var gateAllowedExtraParamKeys = map[string]struct{}{
	"resolution":   {},
	"aspect_ratio": {},
	"metadata":     {},
}

// ToGateVideoGenerationRequest 把 provider-neutral 请求映射为 Gate 请求体。
// duration 来自 Seconds（字符串秒），resolution/aspect_ratio/metadata 来自
// ExtraParams 白名单；未知 ExtraParams key 直接拒绝。
func ToGateVideoGenerationRequest(bifrostReq *schemas.BifrostVideoGenerationRequest) (*GateVideoGenerationRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil || bifrostReq.Input.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	request := &GateVideoGenerationRequest{
		Model:  bifrostReq.Model,
		Prompt: bifrostReq.Input.Prompt,
	}

	// 主参考图（图生视频）映射为首帧参考
	if bifrostReq.Input.InputReference != nil && *bifrostReq.Input.InputReference != "" {
		request.InputReferences = []GateInputReference{{
			Type: "image",
			URL:  *bifrostReq.Input.InputReference,
			Role: "first_frame",
		}}
	}

	if bifrostReq.Params != nil {
		params := bifrostReq.Params
		if params.Seconds != nil && *params.Seconds != "" {
			seconds, err := strconv.Atoi(*params.Seconds)
			if err != nil {
				return nil, fmt.Errorf("invalid seconds value %q: %w", *params.Seconds, err)
			}
			request.Duration = &seconds
		}
		if params.Size != "" {
			request.Size = params.Size
		}
		if params.Audio != nil {
			request.GenerateAudio = params.Audio
		}
		if params.Seed != nil {
			request.Seed = params.Seed
		}

		for key, value := range params.ExtraParams {
			if _, allowed := gateAllowedExtraParamKeys[key]; !allowed {
				return nil, fmt.Errorf("unsupported extra param %q for gate provider", key)
			}
			switch key {
			case "resolution":
				resolution, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("resolution must be a string")
				}
				request.Resolution = resolution
			case "aspect_ratio":
				aspectRatio, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("aspect_ratio must be a string")
				}
				request.AspectRatio = aspectRatio
			case "metadata":
				metadata, err := schemas.ConvertViaJSON[map[string]string](value)
				if err != nil {
					return nil, fmt.Errorf("metadata must be a string map: %w", err)
				}
				request.Metadata = metadata
			}
		}
	}

	return request, nil
}

// mapGateStatus 映射 Gate 任务状态；未知状态是协议错误（fail-closed）。
func mapGateStatus(status string) (schemas.VideoStatus, error) {
	switch status {
	case "pending":
		return schemas.VideoStatusQueued, nil
	case "in_progress":
		return schemas.VideoStatusInProgress, nil
	case "completed":
		return schemas.VideoStatusCompleted, nil
	case "failed":
		return schemas.VideoStatusFailed, nil
	default:
		return "", fmt.Errorf("unknown gate task status %q", status)
	}
}

// parseGateTime 解析 Gate RFC3339 时间；空串返回 nil。格式错误是协议错误。
func parseGateTime(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("invalid gate RFC3339 time %q: %w", value, err)
	}
	unix := parsed.Unix()
	return &unix, nil
}

// gateTerminalCost 把 Gate 终态计费事实映射为归一化上游成本。
// 仅任务终态（completed/failed）且 billing_status=settled、billed_cost 为
// 有限非负数时返回非 nil；其余（非终态、缺失、pre_deducted、未知状态、非法
// 数字）一律 nil，由调用方进入 actual-cost-unknown。Gate 查询响应不返回
// currency，Cost 不携带币种。原始十进制文本仅通过 ExtraFields.RawResponse
// 可选保留，绝不冒充计算依据。
func gateTerminalCost(status, billingStatus, billedCost string) *schemas.BifrostCost {
	if status != "completed" && status != "failed" {
		return nil
	}
	if billingStatus != "settled" {
		return nil
	}
	value, err := strconv.ParseFloat(billedCost, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return nil
	}
	return &schemas.BifrostCost{TotalCost: value}
}

// newGateError 构造携带 Gate HTTP 状态码与 envelope msg 的 provider 错误。
// StatusCode 始终保留，调用方按 StatusCode + Error.Code 做稳定映射。
func newGateError(resp *fasthttp.Response, body []byte) *schemas.BifrostError {
	statusCode := resp.StatusCode()

	message := ""
	var envelope GateEnvelope[struct{}]
	if err := sonic.Unmarshal(body, &envelope); err == nil && envelope.Msg != "" {
		message = envelope.Msg
	}
	if message == "" {
		trimmed := strings.TrimSpace(string(body))
		if len(trimmed) > 256 {
			trimmed = trimmed[:256]
		}
		message = trimmed
	}
	if message == "" {
		message = fmt.Sprintf("gate provider returned HTTP %d", statusCode)
	}

	code := "gate_error"
	switch statusCode {
	case fasthttp.StatusNotFound:
		code = ErrCodeVideoNotFound
	case fasthttp.StatusConflict:
		code = ErrCodeVideoNotReady
	case fasthttp.StatusGone:
		code = ErrCodeVideoExpired
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Code:    &code,
			Message: message,
		},
	}
}

// VideoGeneration submits a video generation task to Gate.
// 幂等键只经既有 BifrostContextKeyExtraHeaders 契约透传为 Gate Idempotency-Key；
// fallback/重试由调用方在 Core 入口层关闭，provider 本身不做任何重试。
func (provider *GateProvider) VideoGeneration(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	gateReq, err := ToGateVideoGenerationRequest(bifrostReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
	}
	jsonData, err := sonic.Marshal(gateReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, gateVideosPath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	// 幂等键经既有 BifrostContextKeyExtraHeaders 契约传递：调用方写入
	// ExtraHeaders["Idempotency-Key"]，SetExtraHeaders 已在上文透传。本 provider
	// 不另设通道。

	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Gate 成功接受返回 202（文档），容忍 200；其余一律 provider 错误
	if resp.StatusCode() != fasthttp.StatusAccepted && resp.StatusCode() != fasthttp.StatusOK {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, newGateError(resp, rawErrBody), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var envelope GateEnvelope[GateVideoSubmitData]
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &envelope, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}
	if envelope.Code != gateEnvelopeCodeSuccess {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("gate submit rejected: envelope code %d, msg %q", envelope.Code, envelope.Msg), nil),
			jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	if envelope.Data == nil || envelope.Data.JobID == "" {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(
			"gate submit response missing job_id", nil),
			jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	data := envelope.Data
	status, err := mapGateStatus(data.Status)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp := &schemas.BifrostVideoGenerationResponse{
		ID:     providerUtils.AddVideoIDProviderSuffix(data.JobID, providerName),
		Model:  bifrostReq.Model,
		Object: "video",
		Status: status,
		// Submit 响应只有 estimated_cost/pre_deduct_amount，没有终态费用；
		// 不映射 Usage（nil 表示尚无 settled 实际费用）。
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}
	if data.Model != "" {
		bifrostResp.Model = data.Model
	}
	if status == schemas.VideoStatusFailed {
		bifrostResp.Error = &schemas.VideoCreateError{Code: "failed", Message: data.Message}
	}

	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// VideoRetrieve retrieves the current state of a Gate video task.
// 终态响应携带完整 billing evidence 与 expires_at；未知状态视为协议错误。
func (provider *GateProvider) VideoRetrieve(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if bifrostReq.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	taskID := providerUtils.StripVideoIDProviderSuffix(bifrostReq.ID, providerName)

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, gateVideosPath+"/"+taskID))
	req.Header.SetMethod(http.MethodGet)
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, newGateError(resp, rawErrBody), nil, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var envelope GateEnvelope[GateVideoRetrieveData]
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &envelope, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency)
	}
	if envelope.Code != gateEnvelopeCodeSuccess || envelope.Data == nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("gate retrieve rejected: envelope code %d, msg %q", envelope.Code, envelope.Msg), nil),
			nil, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	data := envelope.Data
	status, err := mapGateStatus(data.Status)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	createdAt, err := parseGateTime(data.CreatedAt)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	completedAt, err := parseGateTime(data.CompletedAt)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	expiresAt, err := parseGateTime(data.ExpiresAt)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp := &schemas.BifrostVideoGenerationResponse{
		ID:          providerUtils.AddVideoIDProviderSuffix(data.JobID, providerName),
		Model:       data.Model,
		Object:      "video",
		Status:      status,
		CompletedAt: completedAt,
		ExpiresAt:   expiresAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}
	// 只有终态 + settled 的合法费用映射 Usage.Cost；其余 Usage 保持 nil。
	if cost := gateTerminalCost(data.Status, data.BillingStatus, data.BilledCost); cost != nil {
		bifrostResp.Usage = &schemas.VideoUsage{Cost: cost}
	}
	if createdAt != nil {
		bifrostResp.CreatedAt = *createdAt
	}
	if data.Duration > 0 {
		bifrostResp.Seconds = schemas.Ptr(strconv.Itoa(data.Duration))
	}
	if data.DownloadURL != "" {
		bifrostResp.Videos = []schemas.VideoOutput{{
			Type:        schemas.VideoOutputTypeURL,
			URL:         schemas.Ptr(data.DownloadURL),
			ContentType: "video/mp4",
		}}
	}
	if status == schemas.VideoStatusFailed {
		message := envelope.Msg
		if message == "" {
			message = "gate task failed"
		}
		bifrostResp.Error = &schemas.VideoCreateError{Code: "failed", Message: message}
	}

	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}

// VideoDownload fetches video content from Gate.
// 第一跳带 Bearer 请求 content 端点并禁止自动重定向（fasthttp 默认不跟随），
// 只接受 302 且 Location 必须是绝对 HTTPS URL；第二跳用全新请求
// （无 Authorization、无幂等头、无 provider ExtraHeaders）与 SSRF-safe
// dialer。未配置响应阈值时内容经既有 Content []byte 返回；配置阈值时
// 超限响应由既有 large-response context reader 流式返回。
func (provider *GateProvider) VideoDownload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	taskID := providerUtils.StripVideoIDProviderSuffix(request.ID, providerName)

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

	// ---- 第一跳：取 302 Location ----
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, gateVideosPath+"/"+taskID+"/content"))
	req.Header.SetMethod(http.MethodGet)
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	statusCode := resp.StatusCode()
	if statusCode == fasthttp.StatusNotFound || statusCode == fasthttp.StatusConflict || statusCode == fasthttp.StatusGone {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, newGateError(resp, rawErrBody), nil, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}
	// 只接受 302：其余一切状态（含 200 直出内容、301/307 等）一律 fail-closed。
	if statusCode != fasthttp.StatusFound {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, newGateError(resp, rawErrBody), nil, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	location := strings.TrimSpace(string(resp.Header.Peek("Location")))
	if location == "" {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(
			fmt.Sprintf("gate download redirect missing Location header (HTTP %d)", statusCode), nil), latency)
	}
	if err := validateDownloadLocation(location); err != nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(
			fmt.Sprintf("gate download redirect rejected: %v", err), nil), latency)
	}

	// ---- 第二跳：全新无鉴权请求 + SSRF-safe dialer + 流式 body ----
	req2 := fasthttp.AcquireRequest()
	resp2 := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req2)
	// 大响应路径下 resp2 所有权转移给 context 中的 LargeResponseReader
	// （Close 时释放）；其余路径在本函数内释放。
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp2)
		}
	}()

	req2.SetRequestURI(location)
	req2.Header.SetMethod(http.MethodGet)
	// req2 是全新对象：天然不带 Authorization、Idempotency-Key 与 provider
	// ExtraHeaders，第二跳只做无鉴权取流。

	// 复用既有 large-response 契约：配置 BifrostContextKeyLargeResponseThreshold
	// 时启用流式读取并返回改造后的 client；未配置时保持既有 buffered 语义。
	client2 := providerUtils.PrepareResponseStreaming(ctx, provider.downloadClient, resp2)

	latency2, bifrostErr, wait2 := providerUtils.MakeRequestWithContext(ctx, client2, req2, resp2)
	defer wait2()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp2.StatusCode() != fasthttp.StatusOK {
		// 错误体只做有界读取（上限 4 KiB）并关闭流，绝不无界 drain。
		errBody, _ := readLimitedBodyStream(resp2, 4096)
		gateErr := newGateErrorFromStatus(resp2.StatusCode(), errBody)
		return nil, providerUtils.SetErrorLatency(gateErr, latency+latency2)
	}

	// CDN 缺 Content-Type 时补默认 video/mp4，保证既有 finalize helper 与
	// 下游 DTO 都拿到确定 MIME。注意 fasthttp 对缺失的 Content-Type 一律
	// 返回 text/plain 默认值（Peek 也一样），必须先禁用默认值回退再判空。
	resp2.Header.SetNoDefaultContentType(true)
	if len(resp2.Header.ContentType()) == 0 {
		resp2.Header.SetContentType("video/mp4")
	}

	// 既有 finalize helper 统一收口：未超阈值返回 buffered body 写入 Content；
	// 超阈值时 reader 注册进 BifrostContextKeyLargeResponseReader，resp2 所有权
	// 移交 reader（Close 时释放）。
	body, isLarge, finalizeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp2, provider.logger)
	if finalizeErr != nil {
		return nil, providerUtils.SetErrorLatency(finalizeErr, latency+latency2)
	}
	if isLarge {
		respOwned = false
	}

	downloadResp := &schemas.BifrostVideoDownloadResponse{
		VideoID:     providerUtils.AddVideoIDProviderSuffix(taskID, providerName),
		ContentType: string(resp2.Header.ContentType()),
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: (latency + latency2).Milliseconds(),
		},
	}
	if !isLarge {
		downloadResp.Content = body
	}
	return downloadResp, nil
}

// validateDownloadLocation 校验第二跳 Location：绝对 HTTPS URL、无 userinfo。
func validateDownloadLocation(location string) error {
	parsed, err := url.Parse(location)
	if err != nil {
		return fmt.Errorf("invalid redirect URL: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return fmt.Errorf("redirect URL must be absolute")
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("redirect URL scheme %q is not https", parsed.Scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("redirect URL must not contain userinfo")
	}
	return nil
}

// readLimitedBodyStream 从（可能是流的）响应体读取最多 limit 字节用于错误消息。
// 只关闭流，不做无界 drain：半读连接由 fasthttp 在 Close 时拆除而不进池复用。
func readLimitedBodyStream(resp *fasthttp.Response, limit int64) ([]byte, error) {
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		if closer, ok := bodyStream.(io.Closer); ok {
			defer func() { _ = closer.Close() }()
		}
		return io.ReadAll(io.LimitReader(bodyStream, limit))
	}
	body := resp.Body()
	if int64(len(body)) > limit {
		return body[:limit], nil
	}
	return append([]byte(nil), body...), nil
}

// newGateErrorFromStatus 为下载第二跳构造错误：不解析 envelope（CDN 不返回
// Gate 信封），保留 HTTP 状态码与截断后的错误体文本。
func newGateErrorFromStatus(statusCode int, body []byte) *schemas.BifrostError {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = fmt.Sprintf("gate download CDN returned HTTP %d", statusCode)
	}
	code := "gate_download_error"
	if statusCode == fasthttp.StatusGone {
		code = ErrCodeVideoExpired
	} else if statusCode == fasthttp.StatusNotFound {
		code = ErrCodeVideoNotFound
	}
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Code:    &code,
			Message: message,
		},
	}
}
