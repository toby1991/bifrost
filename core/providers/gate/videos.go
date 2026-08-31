package gate

import (
	"compress/gzip"
	"fmt"
	"io"
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

// gateBillingEvidence 组装精确字符串计费证据；全部为空时返回 nil。
func gateBillingEvidence(estimatedCost, billedCost, currency, billingStatus string) *schemas.VideoBillingEvidence {
	if estimatedCost == "" && billedCost == "" && currency == "" && billingStatus == "" {
		return nil
	}
	evidence := &schemas.VideoBillingEvidence{}
	if estimatedCost != "" {
		evidence.EstimatedCost = schemas.Ptr(estimatedCost)
	}
	if billedCost != "" {
		evidence.BilledCost = schemas.Ptr(billedCost)
	}
	if currency != "" {
		evidence.Currency = schemas.Ptr(currency)
	}
	if billingStatus != "" {
		evidence.BillingStatus = schemas.Ptr(billingStatus)
	}
	return evidence
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
// 幂等键从 BifrostContextKeyUpstreamIdempotencyKey 透传为 Gate Idempotency-Key；
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
	// 上游幂等键：仅当调用方显式派生（同一次提交链）时透传
	if idempotencyKey, ok := ctx.Value(schemas.BifrostContextKeyUpstreamIdempotencyKey).(string); ok && idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

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
		ID:              providerUtils.AddVideoIDProviderSuffix(data.JobID, providerName),
		Model:           bifrostReq.Model,
		Object:          "video",
		Status:          status,
		BillingEvidence: gateBillingEvidence(data.EstimatedCost, "", data.Currency, ""),
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
		ID:              providerUtils.AddVideoIDProviderSuffix(data.JobID, providerName),
		Model:           data.Model,
		Object:          "video",
		Status:          status,
		CompletedAt:     completedAt,
		ExpiresAt:       expiresAt,
		BillingEvidence: gateBillingEvidence(data.EstimatedCost, data.BilledCost, data.Currency, data.BillingStatus),
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
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

// VideoDownload streams video content from Gate.
// 第一跳带 Bearer 请求 content 端点并禁止自动重定向（fasthttp 默认不跟随），
// 302 Location 必须是绝对 HTTPS URL；第二跳用全新请求（无 Authorization、
// 无幂等头、无 provider ExtraHeaders）、SSRF-safe dialer 与流式 body 返回。
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
	if statusCode < 300 || statusCode >= 400 {
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
	// resp2 的所有权在成功时转移给 LargeResponseReader（Close 时释放）；
	// 失败路径在本函数内释放。
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp2)
		}
	}()

	req2.SetRequestURI(location)
	req2.Header.SetMethod(http.MethodGet)
	// 明确禁止任何自动 gzip 协商之外的隐式头；不复制 Authorization、
	// Idempotency-Key、ExtraHeaders（req2 是全新对象，天然无这些头）。
	resp2.SkipBody = false
	resp2.StreamBody = true

	latency2, bifrostErr, wait2 := providerUtils.MakeRequestWithContext(ctx, provider.downloadClient, req2, resp2)
	defer wait2()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp2.StatusCode() != fasthttp.StatusOK {
		// 错误体按小文本读取（上限 4 KiB），绝不把错误路径变成大缓冲
		errBody, _ := readLimitedBodyStream(resp2, 4096)
		gateErr := newGateErrorFromStatus(resp2.StatusCode(), errBody)
		return nil, providerUtils.SetErrorLatency(gateErr, latency+latency2)
	}

	// 组装流式 reader：按需叠加 gzip 解压层
	stream := resp2.BodyStream()
	if stream == nil {
		return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(
			"gate download response body stream is unavailable", nil), latency+latency2)
	}
	reader := io.Reader(stream)
	var cleanup func()
	if strings.EqualFold(strings.TrimSpace(string(resp2.Header.Peek("Content-Encoding"))), "gzip") {
		gz, err := gzip.NewReader(stream)
		if err != nil {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(
				schemas.ErrProviderResponseDecode, err), latency+latency2)
		}
		reader = gz
		cleanup = func() { _ = gz.Close() }
	}

	contentType := string(resp2.Header.ContentType())
	if contentType == "" {
		contentType = "video/mp4"
	}

	largeReader := providerUtils.NewLargeResponseReader(reader, resp2, ctx, cleanup)
	respOwned = false

	return &schemas.BifrostVideoDownloadResponse{
		VideoID:       providerUtils.AddVideoIDProviderSuffix(taskID, providerName),
		ContentType:   contentType,
		ContentStream: largeReader,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: (latency + latency2).Milliseconds(),
		},
	}, nil
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
func readLimitedBodyStream(resp *fasthttp.Response, limit int64) ([]byte, error) {
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		defer func() {
			_, _ = io.Copy(io.Discard, bodyStream)
			if closer, ok := bodyStream.(io.Closer); ok {
				_ = closer.Close()
			}
		}()
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
