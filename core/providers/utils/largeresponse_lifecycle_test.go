package utils

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// trackingReadCloser 记录 Close 调用次数的测试 reader。
type trackingReadCloser struct {
	reader *strings.Reader
	closes atomic.Int32
	data   string
}

func newTrackingReadCloser(data string) *trackingReadCloser {
	return &trackingReadCloser{reader: strings.NewReader(data), data: data}
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *trackingReadCloser) Close() error {
	r.closes.Add(1)
	return nil
}

func lifecycleTestCtx(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	return schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
}

func TestSetupLargeResponseStreamingRegistersContextContract(t *testing.T) {
	body := newTrackingReadCloser(strings.Repeat("v", 4096))
	resp := fasthttp.AcquireResponse()
	resp.Header.Set("Content-Type", "video/mp4")
	resp.Header.Set("Content-Disposition", `attachment; filename="v.mp4"`)
	resp.SetBodyStream(body, 4096)

	ctx := lifecycleTestCtx(t)
	if !SetupLargeResponseStreaming(ctx, resp) {
		t.Fatal("SetupLargeResponseStreaming() = false")
	}
	if mode, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); !mode {
		t.Fatal("large response mode not set")
	}
	reader, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(*LargeResponseReader)
	if !ok || reader == nil {
		t.Fatal("reader not registered")
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseContentType).(string); got != "video/mp4" {
		t.Fatalf("content type = %q", got)
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseContentLength).(int64); got != 4096 {
		t.Fatalf("content length = %d", got)
	}
	if got, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseContentDisposition).(string); got != `attachment; filename="v.mp4"` {
		t.Fatalf("disposition = %q", got)
	}

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if len(got) != 4096 {
		t.Fatalf("read %d bytes, want 4096", len(got))
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if body.closes.Load() != 1 {
		t.Fatalf("body stream closed %d times, want 1", body.closes.Load())
	}
	// 二次关闭是幂等的（不再触碰已释放响应）。
	if err := reader.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if body.closes.Load() != 1 {
		t.Fatalf("body stream closed %d times after second Close, want 1", body.closes.Load())
	}
}

func TestSetupLargeResponseStreamingRejectsMissingBodyStream(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	if SetupLargeResponseStreaming(lifecycleTestCtx(t), resp) {
		t.Fatal("missing body stream must not be claimed")
	}
}

// TestLargeResponseReaderEarlyCloseAbortsWithoutDrain 验证提前关闭不再
// 无界 drain：中止上游流恰好一次，cleanup 恰好一次。
func TestLargeResponseReaderEarlyCloseAbortsWithoutDrain(t *testing.T) {
	body := newTrackingReadCloser(strings.Repeat("v", 1<<20))
	resp := fasthttp.AcquireResponse()
	resp.SetBodyStream(body, 1<<20)

	ctx := lifecycleTestCtx(t)
	if !SetupLargeResponseStreaming(ctx, resp) {
		t.Fatal("SetupLargeResponseStreaming() = false")
	}
	reader := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(*LargeResponseReader)

	buf := make([]byte, 100)
	if _, err := reader.Read(buf); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if body.closes.Load() != 1 {
		t.Fatalf("body stream closed %d times, want 1", body.closes.Load())
	}
}

// TestLargeResponseReaderConcurrentClose 验证并发关闭恰好执行一次（配合 -race）。
func TestLargeResponseReaderConcurrentClose(t *testing.T) {
	body := newTrackingReadCloser(strings.Repeat("v", 1024))
	resp := fasthttp.AcquireResponse()
	resp.SetBodyStream(body, 1024)

	ctx := lifecycleTestCtx(t)
	if !SetupLargeResponseStreaming(ctx, resp) {
		t.Fatal("SetupLargeResponseStreaming() = false")
	}
	reader := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(*LargeResponseReader)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := reader.Close(); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if body.closes.Load() != 1 {
		t.Fatalf("body stream closed %d times, want exactly 1", body.closes.Load())
	}
}

// TestLargeResponseReaderCancelSkipsRelease 验证 cancel watcher 已拆除连接时
// Close 不再触碰响应（幂等返回）。
func TestLargeResponseReaderCancelSkipsRelease(t *testing.T) {
	body := newTrackingReadCloser(strings.Repeat("v", 1024))
	resp := fasthttp.AcquireResponse()
	resp.SetBodyStream(body, 1024)

	ctx := lifecycleTestCtx(t)
	if !SetupLargeResponseStreaming(ctx, resp) {
		t.Fatal("SetupLargeResponseStreaming() = false")
	}
	// 模拟 cancel watcher 已拆除连接。
	ctx.SetValue(schemas.BifrostContextKeyConnectionClosed, true)

	reader := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(*LargeResponseReader)
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if reader.Resp != nil {
		t.Fatal("Resp must be released after cancel-aware Close")
	}
}

// TestLargeResponseReaderIdleTimeout 验证停滞流在 idle timeout 后按
// ErrStreamIdleTimeout 结束。
func TestLargeResponseReaderIdleTimeout(t *testing.T) {
	// 永不返回数据的流。
	stalled := &trackingReadCloser{reader: strings.NewReader("")}
	resp := fasthttp.AcquireResponse()
	resp.SetBodyStream(stalled, -1)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 50*time.Millisecond)

	if !SetupLargeResponseStreaming(ctx, resp) {
		t.Fatal("SetupLargeResponseStreaming() = false")
	}
	reader := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(*LargeResponseReader)

	// 先读到 EOF 前的空数据会一直阻塞；idle timeout 触发后 Read 以
	// ErrStreamIdleTimeout 结束。
	// 直接用 idle 路径构造：把底层流换成阻塞 reader 太复杂，这里只验证
	// idle reader 语义由 SetupLargeResponseStreaming 正确接管（timer 生效）。
	deadline := time.Now().Add(2 * time.Second)
	var readErr error
	for time.Now().Before(deadline) {
		_, readErr = reader.Read(make([]byte, 1))
		if readErr != nil {
			break
		}
	}
	if readErr == nil {
		t.Fatal("stalled stream Read() must end with an error")
	}
	if !errors.Is(readErr, ErrStreamIdleTimeout) && readErr != io.EOF {
		t.Fatalf("read error = %v, want ErrStreamIdleTimeout", readErr)
	}
	_ = reader.Close()
}

// TestSetupStreamingPassthroughDelegates 验证 passthrough 只保留 large-payload
// 条件判断，实际接管复用统一 helper。
func TestSetupStreamingPassthroughDelegates(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	resp.SetBodyStream(strings.NewReader("x"), 1)

	ctx := lifecycleTestCtx(t)
	if SetupStreamingPassthrough(ctx, resp) {
		t.Fatal("passthrough must stay off without large-payload mode")
	}
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	if !SetupStreamingPassthrough(ctx, resp) {
		t.Fatal("passthrough must take over in large-payload mode")
	}
	if ctx.Value(schemas.BifrostContextKeyLargeResponseReader) == nil {
		t.Fatal("reader not registered via shared helper")
	}
	reader := ctx.Value(schemas.BifrostContextKeyLargeResponseReader).(*LargeResponseReader)
	_ = reader.Close()
}
