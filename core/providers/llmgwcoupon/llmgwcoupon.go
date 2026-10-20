// Package llmgwcoupon exposes coupon allocation through the existing video task API.
//
// 本 Provider 只领取或查询原码，不认识具体 Agent 页面，也不拼接兑换 URL。
// 完成结果的 videos[].url 承载原码，content_type 固定为 text/plain。
package llmgwcoupon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ClientContextKey carries the host application's reusable coupon RPC adapter.
// SDK 只保存小型接口句柄；gRPC 连接、生成的 proto 和鉴权实现均由宿主负责。
const ClientContextKey schemas.BifrostContextKey = "llmgw-coupon-client"

// idempotencyContextKey is an opt-in internal value, never an automatic header.
// 值为 Execution ID 的十进制字符串；Provider 自行决定如何使用，不改写 Core RequestID。
const idempotencyContextKey schemas.BifrostContextKey = "Idempotency-Key"

// Client decouples the Provider from the host application's generated RPC client.
// 错误保持宿主提供的 BifrostError 分类；通信结果不明确时不得伪造为未领取。
type Client interface {
	Claim(ctx context.Context, endpoint, token, poolCode string, executionID uint64) (uint64, *schemas.BifrostError)
	Get(ctx context.Context, endpoint, token string, claimID uint64) (*Claim, *schemas.BifrostError)
}

// Claim is a persisted allocation returned by the coupon service.
type Claim struct {
	ID          uint64
	PoolCode    string
	ExecutionID uint64
	CodeValue   string
	ClaimedAt   int64
}

// LLMGWCouponProvider implements coupon allocation with the existing video contract.
type LLMGWCouponProvider struct {
	endpoint             string
	requestTimeout       time.Duration
	customProviderConfig *schemas.CustomProviderConfig
}

var _ schemas.Provider = (*LLMGWCouponProvider)(nil)

// NewLLMGWCouponProvider validates the gRPC target; it does not open a connection.
// endpoint 使用 host:port，不接受 HTTP URL，连接由宿主 Client 按 endpoint 复用。
func NewLLMGWCouponProvider(config *schemas.ProviderConfig, _ schemas.Logger) (*LLMGWCouponProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("llmgw-coupon provider config is required")
	}
	endpoint := strings.TrimSpace(config.NetworkConfig.BaseURL)
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return nil, fmt.Errorf("llmgw-coupon base_url must be a gRPC host:port")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 || strings.ContainsAny(host, "/?#@ \t\r\n") {
		return nil, fmt.Errorf("llmgw-coupon base_url must be a gRPC host:port")
	}
	config.CheckAndSetDefaults()
	return &LLMGWCouponProvider{endpoint: endpoint, requestTimeout: time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds) * time.Second, customProviderConfig: config.CustomProviderConfig}, nil
}

func (provider *LLMGWCouponProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.LLMGWCoupon, provider.customProviderConfig)
}

// VideoGeneration allocates one code and returns only its claim ID.
func (provider *LLMGWCouponProvider) VideoGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.LLMGWCoupon, provider.customProviderConfig, schemas.VideoGenerationRequest); err != nil {
		return nil, err
	}
	if request == nil || strings.TrimSpace(request.Model) == "" {
		return nil, notDispatched("coupon pool model is required")
	}
	executionID, bifrostErr := executionIDFromContext(ctx)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	client, token, bifrostErr := clientAndToken(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	started := time.Now()
	rpcCtx, cancel := context.WithTimeout(ctx, provider.requestTimeout)
	defer cancel()
	claimID, bifrostErr := client.Claim(rpcCtx, provider.endpoint, token, request.Model, executionID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if claimID == 0 {
		return nil, providerUtils.NewBifrostOperationError("coupon allocation response missing claim id", nil)
	}
	return &schemas.BifrostVideoGenerationResponse{
		ID:          providerUtils.AddVideoIDProviderSuffix(strconv.FormatUint(claimID, 10), provider.GetProviderKey()),
		Model:       request.Model,
		Object:      "video",
		Status:      schemas.VideoStatusInProgress,
		CreatedAt:   started.Unix(),
		ExtraFields: schemas.BifrostResponseExtraFields{Latency: time.Since(started).Milliseconds()},
	}, nil
}

// VideoRetrieve returns the original code, without allocating another one.
func (provider *LLMGWCouponProvider) VideoRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.LLMGWCoupon, provider.customProviderConfig, schemas.VideoRetrieveRequest); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, notDispatched("coupon claim id is required")
	}
	claimID, err := positiveDecimalID(providerUtils.StripVideoIDProviderSuffix(request.ID, provider.GetProviderKey()))
	if err != nil {
		return nil, notDispatched("coupon claim id must be a positive uint64 decimal string")
	}
	// 独立 SDK 可凭 claim ID + credential 查询；宿主若传入原 Execution ID，必须校验匹配。
	var executionID uint64
	if ctx != nil && ctx.Value(idempotencyContextKey) != nil {
		var bifrostErr *schemas.BifrostError
		executionID, bifrostErr = executionIDFromContext(ctx)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
	}
	client, token, bifrostErr := clientAndToken(ctx, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	started := time.Now()
	rpcCtx, cancel := context.WithTimeout(ctx, provider.requestTimeout)
	defer cancel()
	claim, bifrostErr := client.Get(rpcCtx, provider.endpoint, token, claimID)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	if claim == nil || claim.ID != claimID || (executionID != 0 && claim.ExecutionID != executionID) || claim.ExecutionID == 0 || claim.PoolCode == "" || claim.CodeValue == "" || claim.ClaimedAt <= 0 {
		return nil, providerUtils.NewBifrostOperationError("invalid coupon claim response", nil)
	}
	completedAt := claim.ClaimedAt
	return &schemas.BifrostVideoGenerationResponse{
		ID:          providerUtils.AddVideoIDProviderSuffix(strconv.FormatUint(claim.ID, 10), provider.GetProviderKey()),
		Model:       claim.PoolCode,
		Object:      "video",
		Status:      schemas.VideoStatusCompleted,
		CreatedAt:   claim.ClaimedAt,
		CompletedAt: &completedAt,
		Videos:      []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL, URL: schemas.Ptr(claim.CodeValue), ContentType: "text/plain"}},
		ExtraFields: schemas.BifrostResponseExtraFields{Latency: time.Since(started).Milliseconds()},
	}, nil
}

func executionIDFromContext(ctx *schemas.BifrostContext) (uint64, *schemas.BifrostError) {
	if ctx == nil {
		return 0, notDispatched("coupon execution id is required in context")
	}
	value, ok := ctx.Value(idempotencyContextKey).(string)
	if !ok {
		return 0, notDispatched("coupon execution id must be a positive uint64 decimal string")
	}
	executionID, err := positiveDecimalID(value)
	if err != nil {
		return 0, notDispatched("coupon execution id must be a positive uint64 decimal string")
	}
	return executionID, nil
}

func positiveDecimalID(value string) (uint64, error) {
	// ParseUint 接受前导 +，此处只接受调用方约定的十进制数字，不静默修正输入。
	if value == "" {
		return 0, fmt.Errorf("empty id")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-decimal id")
		}
	}
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid positive id")
	}
	return id, nil
}

func clientAndToken(ctx *schemas.BifrostContext, key schemas.Key) (Client, string, *schemas.BifrostError) {
	if ctx == nil {
		return nil, "", notDispatched("coupon RPC client is missing from context")
	}
	client, ok := ctx.Value(ClientContextKey).(Client)
	if !ok || client == nil {
		return nil, "", notDispatched("coupon RPC client is missing from context")
	}
	token := key.Value.GetValue()
	if token == "" {
		return nil, "", notDispatched("coupon service credential is required")
	}
	return client, token, nil
}

func notDispatched(message string) *schemas.BifrostError {
	statusCode := http.StatusBadRequest
	errorType := "invalid_request_error"
	errorCode := "request_not_dispatched"
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		AllowFallbacks: schemas.Ptr(false),
		Error:          &schemas.ErrorField{Type: &errorType, Code: &errorCode, Message: message},
	}
}
