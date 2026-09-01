package utils

import (
	"bytes"
	"io"
	"math"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// LargeResponseReader wraps an io.Reader and releases the fasthttp response on Close.
// Used by providers to keep the response alive while the transport streams it to the client.
// ctx is held to check BifrostContextKeyConnectionClosed in Close, so a mid-stream
// cancellation that already tore down the underlying fasthttp conn does not double-release.
type LargeResponseReader struct {
	io.Reader
	Resp      *fasthttp.Response
	ctx       *schemas.BifrostContext
	cleanup   func()
	closeOnce sync.Once
	consumed  bool // true after Read returns io.EOF, body fully consumed through Reader chain
}

// Read delegates to the wrapped Reader and tracks EOF so Close() can skip
// a redundant (and potentially blocking) drain of the body stream.
func (r *LargeResponseReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.consumed = true
	}
	return n, err
}

// Close drains any unconsumed body stream and releases the underlying fasthttp
// response back to the pool. Draining prevents "whitespace in header" errors on
// connection reuse when the client disconnects before the full response is consumed
// (see: fasthttp#1743).
//
// When the body was already fully consumed through the Reader chain (consumed == true),
// the drain is skipped. For identity-encoded responses (no Content-Length), the body
// stream is a fasthttp closeReader that blocks until the TCP connection closes — which
// can take minutes if the upstream server keeps the connection alive.
// Close 释放底层 fasthttp 响应并归还 reader 所有权。并发关闭是合法的：
// closeOnce 保证 cleanup 恰好执行一次、响应只释放一次。
//
// 完整读到 EOF：正常释放。未完整消费：调用 resp.CloseBodyStream() 中止
// 上游连接（fasthttp 会关闭该连接而不是无界 drain 或把半读连接放回池），
// 再释放响应对象。连接已被 cancel watcher 拆除时不再触碰响应。
func (r *LargeResponseReader) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		resp := r.Resp
		if resp == nil {
			return
		}
		// Run cleanup first so SetupStreamCancellation's goroutine settles (close(done); <-closed)
		// before we read BifrostContextKeyConnectionClosed. The goroutine's done-branch can set the
		// flag when ctx.Err() != nil, so checking it before cleanup would miss that interleaving and
		// fall through to fasthttp.ReleaseResponse on an already-torn-down conn (nil-deref in connsCleaner).
		if r.cleanup != nil {
			r.cleanup()
			r.cleanup = nil
		}
		if r.ctx != nil {
			if closed, ok := r.ctx.Value(schemas.BifrostContextKeyConnectionClosed).(bool); ok && closed {
				r.Resp = nil
				return
			}
		}
		r.Resp = nil
		// CloseBodyStream 在 EOF 后是廉价的干净关闭；未完整消费时中止上游
		// 连接，绝不 drain 整个大响应体。
		_ = resp.CloseBodyStream()
		fasthttp.ReleaseResponse(resp)
	})
	return nil
}

// BuildLargeResponseClient creates a streaming-enabled fasthttp client for large response detection.
// The client caps buffering at the threshold and enables response body streaming.
//
// ReadTimeout/WriteTimeout/MaxConnDuration are zeroed: large-response bodies may take arbitrarily
// long to download, and fasthttp's ReadTimeout bounds *full* body read — not idle. Idle detection
// on stalled streams is handled separately (see NewIdleTimeoutReader / SetupStreamingPassthrough).
func BuildLargeResponseClient(base *fasthttp.Client, responseThreshold int64) *fasthttp.Client {
	client := CloneFastHTTPClientConfig(base)
	if responseThreshold > 0 && responseThreshold <= int64(math.MaxInt) {
		client.MaxResponseBodySize = int(responseThreshold)
	}
	client.StreamResponseBody = true
	client.ReadTimeout = 0
	client.WriteTimeout = 0
	client.MaxConnDuration = 0
	return client
}

// PrepareResponseStreaming configures response body streaming when a large response
// threshold is set in context. Returns the client to use for MakeRequestWithContext.
// When threshold > 0: sets resp.StreamBody = true and returns a streaming-enabled client.
// When threshold <= 0: returns the original client unchanged (no-op for feature-off path).
func PrepareResponseStreaming(ctx *schemas.BifrostContext, client *fasthttp.Client, resp *fasthttp.Response) *fasthttp.Client {
	responseThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseThreshold).(int64)
	if responseThreshold <= 0 {
		return client
	}
	resp.StreamBody = true
	return BuildLargeResponseClient(client, responseThreshold)
}

// MaterializeStreamErrorBody reads a streamed error body into resp so that resp.Body()
// returns the error payload for parsing. No-op when response streaming is not active.
func MaterializeStreamErrorBody(ctx *schemas.BifrostContext, resp *fasthttp.Response) {
	responseThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseThreshold).(int64)
	if responseThreshold <= 0 {
		return
	}
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		gz, reader, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
		if wasGzip {
			defer ReleaseGzipReader(gz)
		}
		bodyBytes, readErr := io.ReadAll(io.LimitReader(reader, 512*1024)) // 512KB cap for error bodies
		if readErr != nil {
			return
		}
		resp.SetBody(bodyBytes)
	}
}

// FinalizeResponseWithLargeDetection processes the response body with optional large response
// detection. Takes ownership semantics: when isLargeResponse is true, the caller must NOT
// release resp (it's wrapped in a reader stored in context). When false, resp is unchanged
// and the caller should release as normal.
//
// Returns:
//   - (body, false, nil) — normal path; body ready for parsing; resp NOT released.
//   - (nil, true, nil) — large response detected; context keys set for streaming;
//     caller must set respOwned = false.
//   - (nil, false, err) — error; resp NOT released.
func FinalizeResponseWithLargeDetection(
	ctx *schemas.BifrostContext,
	resp *fasthttp.Response,
	logger schemas.Logger,
) ([]byte, bool, *schemas.BifrostError) {
	responseThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseThreshold).(int64)

	// No threshold — normal buffered read (feature-off path)
	if responseThreshold <= 0 {
		body, err := CheckAndDecodeBody(resp)
		if err != nil {
			return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		// Copy body before caller releases resp
		return append([]byte(nil), body...), false, nil
	}

	contentLength := resp.Header.ContentLength()

	// Known small response — read from stream, return body for normal parsing
	if contentLength > 0 && int64(contentLength) <= responseThreshold {
		if bodyStream := resp.BodyStream(); bodyStream != nil {
			gz, reader, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
			if wasGzip {
				defer ReleaseGzipReader(gz)
			}
			bodyBytes, readErr := io.ReadAll(reader)
			if readErr != nil {
				return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, readErr)
			}
			return bodyBytes, false, nil
		}
		// No stream — buffered fallback
		body, err := CheckAndDecodeBody(resp)
		if err != nil {
			return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		return append([]byte(nil), body...), false, nil
	}

	// Unknown Content-Length (chunked transfer encoding) — buffer up to responseThreshold
	// to determine if response is truly large. Responses within threshold are returned
	// buffered for normal parsing/logging; only responses exceeding threshold are streamed.
	if contentLength <= 0 {
		if bodyStream := resp.BodyStream(); bodyStream != nil {
			gz, reader, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
			releaseGzip := func() {}
			if wasGzip {
				releaseGzip = func() {
					ReleaseGzipReader(gz)
				}
			}
			bodyBytes, readErr := io.ReadAll(io.LimitReader(reader, responseThreshold+1))
			if readErr != nil {
				releaseGzip()
				return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, readErr)
			}
			if int64(len(bodyBytes)) <= responseThreshold {
				releaseGzip()
				return bodyBytes, false, nil
			}
			// Exceeds threshold without Content-Length — set up large response streaming.
			combinedReader := io.MultiReader(bytes.NewReader(bodyBytes), reader)
			closableReader := &LargeResponseReader{
				Reader:  combinedReader,
				Resp:    resp,
				ctx:     ctx,
				cleanup: releaseGzip,
			}
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseMode, true)
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseReader, closableReader)
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentLength, contentLength)
			if ct := string(resp.Header.ContentType()); ct != "" {
				ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentType, ct)
			}
			previewLen := min(len(bodyBytes), 1048576)
			ctx.SetValue(schemas.BifrostContextKeyLargePayloadResponsePreview, string(bodyBytes[:previewLen]))
			return nil, true, nil
		}
		// No stream — buffered fallback
		body, err := CheckAndDecodeBody(resp)
		if err != nil {
			return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		return append([]byte(nil), body...), false, nil
	}

	// Known large response (Content-Length > threshold) — prefetch first 64KB for
	// metadata extraction, then stream the rest without full materialization.
	bodyStream := resp.BodyStream()
	if bodyStream == nil {
		// No stream available — fall back to buffered read
		if logger != nil {
			logger.Warn("large-response fallback to buffered path: content_length=%d threshold=%d body_stream_nil=true", contentLength, responseThreshold)
		}
		body, err := CheckAndDecodeBody(resp)
		if err != nil {
			return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		}
		return append([]byte(nil), body...), false, nil
	}

	// Decompress on-the-fly if provider returned gzip-encoded response.
	// Clears Content-Encoding so the transport doesn't re-add it to the client response.
	gz, decompressedStream, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
	if wasGzip {
		contentLength = -1 // decompressed size unknown; transport will use chunked encoding
	}

	prefetchSize := 64 * 1024 // default
	if ps, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadPrefetchSize).(int); ok && ps > 0 {
		prefetchSize = ps
	}
	prefetchBuf := make([]byte, prefetchSize)
	n, readErr := io.ReadFull(decompressedStream, prefetchBuf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		if wasGzip {
			ReleaseGzipReader(gz)
		}
		return nil, false, NewBifrostOperationError(schemas.ErrProviderResponseDecode, readErr)
	}
	prefetchBuf = prefetchBuf[:n]

	combinedReader := io.MultiReader(bytes.NewReader(prefetchBuf), decompressedStream)
	closableReader := &LargeResponseReader{
		Reader: combinedReader,
		Resp:   resp,
		ctx:    ctx,
		cleanup: func() {
			if wasGzip {
				ReleaseGzipReader(gz)
			}
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyLargeResponseMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseReader, closableReader)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentLength, contentLength)
	if ct := string(resp.Header.ContentType()); ct != "" {
		ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentType, ct)
	}
	previewLen := min(n, 1048576)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadResponsePreview, string(prefetchBuf[:previewLen]))

	return nil, true, nil
}

// ParseOpenAIUsageFromBytes parses OpenAI-format usage from raw JSON bytes into BifrostLLMUsage.
// Handles both Chat Completions (prompt_tokens/completion_tokens) and Responses API
// (input_tokens/output_tokens) field names. Expects the "usage" object bytes directly,
// not the full response body.
func ParseOpenAIUsageFromBytes(data []byte) *schemas.BifrostLLMUsage {
	var usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		// Responses API uses different field names
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	if err := sonic.Unmarshal(data, &usage); err != nil {
		return nil
	}

	result := &schemas.BifrostLLMUsage{}
	if usage.PromptTokens > 0 {
		result.PromptTokens = usage.PromptTokens
	} else if usage.InputTokens > 0 {
		result.PromptTokens = usage.InputTokens
	}
	if usage.CompletionTokens > 0 {
		result.CompletionTokens = usage.CompletionTokens
	} else if usage.OutputTokens > 0 {
		result.CompletionTokens = usage.OutputTokens
	}
	if usage.TotalTokens > 0 {
		result.TotalTokens = usage.TotalTokens
	} else {
		result.TotalTokens = result.PromptTokens + result.CompletionTokens
	}

	if result.TotalTokens == 0 {
		return nil
	}
	return result
}

// SetupStreamingPassthrough configures large response passthrough for streaming
// responses when large payload mode is active. 条件判断保留在本函数：仅
// large-payload 模式下才接管，实际接管逻辑统一走 SetupLargeResponseStreaming。
// 返回 true 时调用方应返回已关闭的 channel，且不得再 release resp。
func SetupStreamingPassthrough(ctx *schemas.BifrostContext, resp *fasthttp.Response) bool {
	isLargePayload, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool)
	if !isLargePayload {
		return false
	}
	return SetupLargeResponseStreaming(ctx, resp)
}

// SetupLargeResponseStreaming 是 provider 自建流式响应的统一接管入口：
// 校验 body stream 存在；完成 gzip 解压、idle timeout、context cancellation
// watcher、content length/type/disposition 注册，并把 *LargeResponseReader
// 放进 BifrostContextKeyLargeResponseReader。构造细节（gzip/idle/cancel
// wiring）不暴露给 provider。
//
// 返回 true 时 resp 的所有权已转移给 context 中的 reader，调用方不得再
// release resp；返回 false 时 resp 未被动过，调用方按普通路径处理。
func SetupLargeResponseStreaming(ctx *schemas.BifrostContext, resp *fasthttp.Response) bool {
	bodyStream := resp.BodyStream()
	if bodyStream == nil {
		return false
	}

	// DecompressStreamBody 会清除 Content-Encoding 头；先记录 gzip 标记。
	wasGzip := len(resp.Header.ContentEncoding()) > 0
	reader, releaseGzip := DecompressStreamBody(resp)

	// Wrap reader with idle timeout to detect stalled streams.
	reader, stopIdleTimeout := NewIdleTimeoutReader(reader, bodyStream, GetStreamIdleTimeout(ctx), ctx)

	// Wire cancellation to the raw fasthttp body. On a mid-stream client disconnect this fires
	// wce.CloseWithError(ctx.Err()) to unblock the transport's Read and sets
	// BifrostContextKeyConnectionClosed so LargeResponseReader.Close skips the double release.
	// logger arg is unused inside SetupStreamCancellation (uses package getLogger), nil is safe.
	stopCancellation := SetupStreamCancellation(ctx, bodyStream, nil)

	closableReader := &LargeResponseReader{
		Reader: reader,
		Resp:   resp,
		ctx:    ctx,
		cleanup: func() {
			stopCancellation()
			stopIdleTimeout()
			releaseGzip()
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyLargeResponseMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargeResponseReader, closableReader)
	// gzip 解压后真实长度未知；只有原始 Content-Length 才有意义。
	if !wasGzip {
		if contentLength := resp.Header.ContentLength(); contentLength > 0 {
			ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentLength, int64(contentLength))
		}
	}
	if ct := string(resp.Header.ContentType()); ct != "" {
		ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentType, ct)
	}
	if disposition := resp.Header.Peek("Content-Disposition"); len(disposition) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyLargeResponseContentDisposition, string(disposition))
	}
	return true
}
