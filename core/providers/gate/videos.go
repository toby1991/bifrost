package gate

import (
	"context"
	"fmt"
	"io"
	"net"
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

// gateDownloadResponseSpill 限制 hop2 在交出 body stream 前的预读缓冲；
// 同一上限用于 CDN 错误消息。
const gateDownloadResponseSpill = 4 * 1024

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
		if params.NegativePrompt != nil {
			return nil, fmt.Errorf("negative_prompt is not supported by gate provider")
		}
		if params.VideoURI != nil {
			return nil, fmt.Errorf("video_uri is not supported by gate provider")
		}
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
			default:
				return nil, fmt.Errorf("unsupported extra param %q for gate provider", key)
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

// newRequestNotDispatchedError 构造"请求未发出"的预分发错误。
// 该 code 只允许在任何字节发网之前产生（请求转换/请求序列化失败）；
// 下游凭 Error.Code=request_not_dispatched 判定可安全退款。收到响应后的
// 解析失败（ErrProviderResponseDecode 等）绝不得使用此 code。
func newRequestNotDispatchedError(message string, err error) *schemas.BifrostError {
	statusCode := http.StatusBadRequest
	errorType := "invalid_request_error"
	errorCode := "request_not_dispatched"
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Type:    &errorType,
			Code:    &errorCode,
			Message: message,
			Error:   err,
		},
	}
}

// VideoGeneration submits a video generation task to Gate.
// 幂等键只经既有 BifrostContextKeyExtraHeaders 契约透传为 Gate Idempotency-Key；
// fallback/重试由调用方在 Core 入口层关闭，provider 本身不做任何重试。
func (provider *GateProvider) VideoGeneration(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	// 先清 sidecar 再做准入：被拒绝的调用也不得泄漏上一次提交账单。
	clearVideoSubmissionBilling(ctx)

	if err := providerUtils.CheckOperationAllowed(schemas.Gate, provider.customProviderConfig, schemas.VideoGenerationRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// 预分发失败（未发出任何字节）：标记 request_not_dispatched，
	// 下游据此安全退还预留额度。
	gateReq, err := ToGateVideoGenerationRequest(bifrostReq)
	if err != nil {
		return nil, newRequestNotDispatchedError(schemas.ErrRequestBodyConversion, err)
	}
	jsonData, err := sonic.Marshal(gateReq)
	if err != nil {
		return nil, newRequestNotDispatchedError(schemas.ErrProviderRequestMarshal, err)
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

	// Gate 文档只定义 202 为成功接受，其余状态一律 fail-closed。
	if resp.StatusCode() != fasthttp.StatusAccepted {
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
	if data.Currency != "" {
		setVideoSubmissionBilling(ctx, bifrostResp.ID, data.Currency)
	}

	return bifrostResp, nil
}

// VideoRetrieve retrieves the current state of a Gate video task.
// 费用不扩展 Core response schema；合法的终态 settled 金额只写入
// Gate-local context sidecar，由嵌入式调用方立即读取。
func (provider *GateProvider) VideoRetrieve(ctx *schemas.BifrostContext, key schemas.Key, bifrostReq *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	// context 可被上层复用；任何失败或非 settled 响应都不得泄漏上一次账单。
	clearVideoSettledBilling(ctx)

	if err := providerUtils.CheckOperationAllowed(schemas.Gate, provider.customProviderConfig, schemas.VideoRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if bifrostReq == nil || bifrostReq.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	taskID := providerUtils.StripVideoIDProviderSuffix(bifrostReq.ID, providerName)
	if taskID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, gateVideosPath+"/"+url.PathEscape(taskID)))
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
	if data.JobID == "" || data.JobID != taskID {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(
			fmt.Sprintf("gate retrieve job_id mismatch: requested %q, received %q", taskID, data.JobID), nil),
			nil, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	status, err := mapGateStatus(data.Status)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, body, sendBackRawRequest, sendBackRawResponse, latency)
	}

	createdAt, err := parseGateTime(data.CreatedAt)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, body, sendBackRawRequest, sendBackRawResponse, latency)
	}
	terminal := status == schemas.VideoStatusCompleted || status == schemas.VideoStatusFailed
	var completedAt, expiresAt *int64
	if terminal {
		completedAt, err = parseGateTime(data.CompletedAt)
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, body, sendBackRawRequest, sendBackRawResponse, latency)
		}
		expiresAt, err = parseGateTime(data.ExpiresAt)
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), nil, body, sendBackRawRequest, sendBackRawResponse, latency)
		}
	}

	bifrostResp := &schemas.BifrostVideoGenerationResponse{
		ID:          providerUtils.AddVideoIDProviderSuffix(taskID, providerName),
		Model:       data.Model,
		Object:      "video",
		Status:      status,
		CompletedAt: completedAt,
		ExpiresAt:   expiresAt,
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
	if data.Resolution != "" {
		bifrostResp.Size = data.Resolution
	}
	// 完成后取 /content 第一跳的临时直链。任何取链失败只让 Videos 留空，
	// 不得把已完成的生成结果或可信 settled 金额改写成失败。
	if status == schemas.VideoStatusCompleted {
		if directURL := provider.fetchContentDirectURL(ctx, key, taskID); directURL != "" {
			bifrostResp.Videos = []schemas.VideoOutput{{
				Type:        schemas.VideoOutputTypeURL,
				URL:         schemas.Ptr(directURL),
				ContentType: "video/mp4",
			}}
		}
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

	// 必须是成功返回前的最后一次可观测写入：早于此处的任何错误
	// 都会保持入口时的 cleared 状态。
	if terminal && data.BillingStatus == "settled" && isNonNegativeDecimal(data.BilledCost) {
		setVideoSettledBilling(ctx, bifrostResp.ID, data.BilledCost, data.Currency)
	}
	return bifrostResp, nil
}

// fetchContentDirectURL 返回 completed 任务的临时下载直链，仅用于
// VideoRetrieve 结果投影。只做 /content 第一跳：使用 provider 私有的
// net/http client（DisableKeepAlives，连接用后即关绝不回池，未读的 302
// 正文不会污染任何复用连接），携带服务端凭证、只读响应头、CheckRedirect
// 禁止跟随重定向；只接受 302 且 Location 通过 validateDownloadLocation。
// 单次预算 = min(2s, provider 请求超时, 外层 context 剩余-100ms)；外层剩余
// 不足 100ms 时整体跳过。任何失败返回空串，由调用方保持 Videos 留空。
func (provider *GateProvider) fetchContentDirectURL(ctx *schemas.BifrostContext, key schemas.Key, taskID string) string {
	// 构造期 fail-closed（如 CA/proxy secret 引用解析为空）：取链永远失败，
	// 但绝不影响 VideoRetrieve 主流程。
	if provider.retrieveLinkErr != nil || provider.retrieveLinkClient == nil {
		return ""
	}

	// 单次取链预算：2s、provider 请求超时、外层 context 剩余（留 100ms
	// 余量）三者取最小；外层剩余不足 100ms 时跳过本次取链。
	budget := retrieveLinkMaxBudget
	if provider.requestTimeout < budget {
		budget = provider.requestTimeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline) - retrieveLinkDeadlineReserve
		if remaining <= 0 {
			return ""
		}
		if remaining < budget {
			budget = remaining
		}
	}
	// 私有 DialContext 从 Value 取回原始短 context，取消时关闭实际连接，
	// 避免 net/http 为连接复用而脱离请求取消，遗留 TLS/代理握手。
	lookupCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	requestCtx := context.WithValue(lookupCtx, retrieveLinkContextKey{}, lookupCtx)

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet,
		provider.networkConfig.BaseURL+providerUtils.GetPathFromContext(ctx, gateVideosPath+"/"+url.PathEscape(taskID)+"/content"), nil)
	if err != nil {
		return ""
	}
	setRetrieveLinkExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders)
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	resp, err := provider.retrieveLinkClient.Do(req)
	if err != nil {
		return ""
	}
	// 302 可能携带正文：不读取也不 drain，直接 Close。DisableKeepAlives
	// 保证未读字节随连接关闭被丢弃，绝不会污染任何复用连接。
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		return ""
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" || validateDownloadLocation(location) != nil {
		return ""
	}
	return location
}

// VideoDownload fetches video content from Gate.
// 第一跳带 Bearer 请求 content 端点并禁止自动重定向（fasthttp 默认不跟随），
// 只接受 302 且 Location 必须是绝对 HTTPS URL；第二跳用全新请求
// （无 Authorization、无幂等头、无 provider ExtraHeaders）与 SSRF-safe
// dialer。未配置响应阈值时内容经既有 Content []byte 返回；配置阈值时
// 超限响应由既有 large-response context reader 流式返回。
func (provider *GateProvider) VideoDownload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Gate, provider.customProviderConfig, schemas.VideoDownloadRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if request == nil || request.ID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}
	taskID := providerUtils.StripVideoIDProviderSuffix(request.ID, providerName)
	if taskID == "" {
		return nil, providerUtils.NewBifrostOperationError("video_id is required", nil)
	}

	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

	// ---- 第一跳：取 302 Location ----
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, gateVideosPath+"/"+url.PathEscape(taskID)+"/content"))
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
	req2.SetConnectionClose()
	// req2 是全新对象：天然不带 Authorization、Idempotency-Key 与 provider
	// ExtraHeaders，第二跳只做无鉴权取流。

	// 每次下载使用独立 client 与有界 context。拨号器用该 context
	// 覆盖 DNS/TCP；request timeout 覆盖 TLS、响应头与 prefetch。
	hop2Ctx, cancelHop2 := context.WithTimeout(ctx, provider.downloadTimeout)
	defer cancelHop2()
	client2, clearHop2Deadline := provider.newDownloadClient(hop2Ctx)

	// hop2 总是以 stream 形式收头，这样非 200 错误体在未配置
	// threshold 时也不会先被 fasthttp 全量缓冲。成功体仍交给 .2
	// Prepare/Finalize，由既有契约决定 buffered 或 context reader。
	resp2.StreamBody = true
	responseThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseThreshold).(int64)
	if responseThreshold > 0 {
		client2 = providerUtils.PrepareResponseStreaming(ctx, client2, resp2)
	}
	// 已知 Content-Length 时，fasthttp 只有遇到 MaxResponseBodySize
	// 才会从全量缓冲切换为 body stream。此值必须在 Prepare 之后设置。
	client2.MaxResponseBodySize = gateDownloadResponseSpill
	if deadline, ok := hop2Ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			req2.SetTimeout(remaining)
		}
	}

	latency2, bifrostErr, wait2 := providerUtils.MakeRequestWithContext(hop2Ctx, client2, req2, resp2)
	defer wait2()
	if bifrostErr != nil {
		return nil, providerUtils.SetErrorLatency(bifrostErr, latency+latency2)
	}

	if resp2.StatusCode() != fasthttp.StatusOK {
		// 错误体只做有界读取（上限 4 KiB），然后中止连接；
		// 绝不 drain 剩余响应。
		errBody := readAndAbortBodyStream(resp2, gateDownloadResponseSpill)
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

	var body []byte
	var isLarge bool
	if responseThreshold > 0 {
		// 配置 threshold 时严格复用 .2 finalize 契约。
		var finalizeErr *schemas.BifrostError
		body, isLarge, finalizeErr = providerUtils.FinalizeResponseWithLargeDetection(ctx, resp2, provider.logger)
		if finalizeErr != nil {
			return nil, providerUtils.SetErrorLatency(finalizeErr, latency+latency2)
		}
	} else {
		// feature-off 仍先以 stream 收取，避免错误响应被全量缓冲。
		// 成功体在 provider 内完整读取；读取错误不得被当成视频字节返回。
		reader, releaseDecompressor := providerUtils.DecompressStreamBody(resp2)
		var readErr error
		body, readErr = io.ReadAll(reader)
		releaseDecompressor()
		if readErr != nil {
			resp2.SetConnectionClose()
			_ = resp2.CloseBodyStream()
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostOperationError(
				schemas.ErrProviderResponseDecode, fmt.Errorf("read gate download body: %w", readErr)), latency+latency2)
		}
	}
	if isLarge {
		// Finalize 已把 resp2 放进 context reader；从这一刻起所有权不可回退。
		respOwned = false
		// Request timeout only protects dial/headers/prefetch. Once the existing
		// .2 reader owns the response, restore .2's unbounded stream lifetime;
		// caller Close/cancellation semantics remain the native Core contract.
		if err := clearHop2Deadline(); err != nil {
			// 失败时保留原 deadline；不能因清理失败再次释放已转移的 resp2。
			if provider.logger != nil {
				provider.logger.Warn("failed to clear gate download stream deadline: %v", err)
			}
		}
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

// newDownloadClient 为一次 hop2 构造独立的 streaming client。该次
// context 约束 DNS/TCP；返回的 cleanup 在 large reader 交接前清除
// fasthttp request timeout 留在连接上的绝对 deadline。
func (provider *GateProvider) newDownloadClient(ctx context.Context) (*fasthttp.Client, func() error) {
	client := providerUtils.CloneFastHTTPClientConfig(provider.downloadClient)
	client.StreamResponseBody = true
	client.ReadTimeout = 0
	client.WriteTimeout = 0
	client.MaxConnDuration = 0
	var connection net.Conn
	client.Dial = func(addr string) (netConn net.Conn, err error) {
		conn, err := provider.downloadDial(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		connection = conn
		return conn, nil
	}
	return client, func() error {
		if connection == nil {
			return nil
		}
		return connection.SetDeadline(time.Time{})
	}
}

// readAndAbortBodyStream 只读取 limit 字节，随后把 response 标记为
// Connection: close 并通过 Response API 单次关闭。CloseBodyStream 会断开
// 未读完的连接并将内部 stream 指针置 nil，后续 Release 不会二次 close。
func readAndAbortBodyStream(resp *fasthttp.Response, limit int64) []byte {
	bodyStream := resp.BodyStream()
	if bodyStream == nil {
		body := resp.Body()
		if int64(len(body)) > limit {
			body = body[:limit]
		}
		return append([]byte(nil), body...)
	}

	body, _ := io.ReadAll(io.LimitReader(bodyStream, limit))
	resp.SetConnectionClose()
	_ = resp.CloseBodyStream()
	return body
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
