package llmgwcoupon

import (
	"context"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ListModels is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ListModelsRequest, provider.GetProviderKey())
}

// ModelRetrieve 未开放；新版 Provider 接口的新增能力仍在发网前拒绝。
func (provider *LLMGWCouponProvider) ModelRetrieve(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostModelRetrieveRequest) (*schemas.BifrostModelRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ModelRetrieveRequest, provider.GetProviderKey())
}

// TextCompletion is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionRequest, provider.GetProviderKey())
}

// TextCompletionStream is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TextCompletionStreamRequest, provider.GetProviderKey())
}

// ChatCompletion is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionRequest, provider.GetProviderKey())
}

// ChatCompletionStream is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ChatCompletionStreamRequest, provider.GetProviderKey())
}

// Responses is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesRequest, provider.GetProviderKey())
}

// ResponsesStream is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ResponsesStreamRequest, provider.GetProviderKey())
}

// CountTokens is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// Compaction is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// Embedding is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Rerank is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.RerankRequest, provider.GetProviderKey())
}

// Decision 未开放；新版 Provider 接口的新增能力仍在发网前拒绝。
func (provider *LLMGWCouponProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, provider.GetProviderKey())
}

// OCR is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// Speech is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationRequest, provider.GetProviderKey())
}

// ImageGenerationStream is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, provider.GetProviderKey())
}

// ImageEdit is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditRequest, provider.GetProviderKey())
}

// ImageEditStream is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// VideoEdit is not supported by the LLMGW Coupon provider（V1 不做视频编辑）。
func (provider *LLMGWCouponProvider) VideoEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoEditRequest) (*schemas.BifrostVideoEditResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoEditRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by the LLMGW Coupon provider（V1 不做 cancel/delete）。
func (provider *LLMGWCouponProvider) VideoDelete(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by the LLMGW Coupon provider（V1 不做 list）。
func (provider *LLMGWCouponProvider) VideoList(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by the LLMGW Coupon provider（V1 不做 remix）。
func (provider *LLMGWCouponProvider) VideoRemix(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchDelete is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) BatchDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileUploadRequest, provider.GetProviderKey())
}

// FileList is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileListRequest, provider.GetProviderKey())
}

// FileRetrieve is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileRetrieveRequest, provider.GetProviderKey())
}

// FileDelete is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileDeleteRequest, provider.GetProviderKey())
}

// FileContent is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

// CachedContentCreate is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) CachedContentCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCachedContentCreateRequest) (*schemas.BifrostCachedContentCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentCreateRequest, provider.GetProviderKey())
}

// CachedContentList is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) CachedContentList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentListRequest) (*schemas.BifrostCachedContentListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentListRequest, provider.GetProviderKey())
}

// CachedContentRetrieve is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) CachedContentRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentRetrieveRequest) (*schemas.BifrostCachedContentRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentRetrieveRequest, provider.GetProviderKey())
}

// CachedContentUpdate is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) CachedContentUpdate(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentUpdateRequest) (*schemas.BifrostCachedContentUpdateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentUpdateRequest, provider.GetProviderKey())
}

// CachedContentDelete is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) CachedContentDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostCachedContentDeleteRequest) (*schemas.BifrostCachedContentDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CachedContentDeleteRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerFileCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerFileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerFileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerFileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) ContainerFileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) Passthrough(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

// PassthroughStream is not supported by the LLMGW Coupon provider.
func (provider *LLMGWCouponProvider) PassthroughStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}

// VideoDownload 未开放；兑换码不是可供下载的视频文件。
func (provider *LLMGWCouponProvider) VideoDownload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}
