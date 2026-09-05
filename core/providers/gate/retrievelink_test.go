package gate

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestGateRetrieveLinkDelayedBodyKeepsPoolClean 锁定：302 第一跳的正文延迟
// 到达（且永不完整）时，取链只读响应头就返回 Location；随后同一 provider 的
// Retrieve 与 Submit 都必须干净解析（取链绝不污染共享连接池）。
func TestGateRetrieveLinkDelayedBodyKeepsPoolClean(t *testing.T) {
	const location = "https://cdn.example.com/signed-direct.mp4"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/content"):
			// 声明大 Content-Length 但只推响应头，正文直到客户端断开才"结束"。
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(8*1024*1024))
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusFound)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"code":200,"msg":"","data":{"job_id":"j_post","status":"pending","model":"m","currency":"USD"}}`)
		default:
			fmt.Fprint(w, gateRetrieveEnvelope("j1", "completed", "settled", "1.0800000001", true))
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	key := testKey("gate-secret")

	// 第一次 retrieve 触发取链：正文延迟也必须只凭响应头拿到 Location。
	ctx1 := testCtx()
	resp1, bifrostErr := provider.VideoRetrieve(ctx1, key, &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}
	if len(resp1.Videos) != 1 || resp1.Videos[0].URL == nil || *resp1.Videos[0].URL != location {
		t.Fatalf("Videos = %+v, want direct URL %q", resp1.Videos, location)
	}

	// 后续 Retrieve 复用主连接池：若取链污染了共享连接，这里会出现解析错误。
	ctx2 := testCtx()
	resp2, bifrostErr := provider.VideoRetrieve(ctx2, key, &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("second VideoRetrieve() error = %+v", bifrostErr.Error)
	}
	if resp2.Status != schemas.VideoStatusCompleted || resp2.ID != "j1:gate" {
		t.Fatalf("second retrieve = %+v", resp2)
	}
	if _, cost, _, ok := SettledVideoBillingFromContext(ctx2); !ok || cost != "1.0800000001" {
		t.Fatalf("settled billing = cost=%q ok=%v, want exact amount kept", cost, ok)
	}

	// 后续 Submit 同样必须干净解析。
	ctx3 := testCtx()
	gen, bifrostErr := provider.VideoGeneration(ctx3, key, &schemas.BifrostVideoGenerationRequest{
		Model: "m",
		Input: &schemas.VideoGenerationInput{Prompt: "a cat"},
	})
	if bifrostErr != nil {
		t.Fatalf("VideoGeneration() error = %+v", bifrostErr.Error)
	}
	if gen.ID != "j_post:gate" || gen.Status != schemas.VideoStatusQueued {
		t.Fatalf("submit response = %+v", gen)
	}
}

// TestGateRetrieveLinkSlowHeadersKeepsSettledBilling 锁定：/content 响应头
// 超过单次取链预算时，VideoRetrieve 仍返回终态与可信 settled 金额，
// Videos 留空。
func TestGateRetrieveLinkSlowHeadersKeepsSettledBilling(t *testing.T) {
	var contentHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			atomic.AddInt32(&contentHits, 1)
			// 响应头比取链预算（requestTimeout=1s 时预算为 1s）更慢；
			// 客户端超时断开后 handler 立即返回。
			select {
			case <-time.After(2 * time.Second):
				w.Header().Set("Location", "https://cdn.example.com/v.mp4")
				w.WriteHeader(http.StatusFound)
			case <-r.Context().Done():
			}
			return
		}
		fmt.Fprint(w, gateRetrieveEnvelope("j1", "completed", "settled", "1.0800000001", true))
	}))
	defer server.Close()

	provider := newTestProviderWithTimeout(t, server.URL, 1)
	ctx := testCtx()
	resp, bifrostErr := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("slow link lookup must not fail retrieve, got %+v", bifrostErr.Error)
	}
	if resp.Status != schemas.VideoStatusCompleted {
		t.Fatalf("Status = %q, want completed", resp.Status)
	}
	if len(resp.Videos) != 0 {
		t.Fatalf("Videos = %+v, want empty on link budget exhaustion", resp.Videos)
	}
	if _, cost, _, ok := SettledVideoBillingFromContext(ctx); !ok || cost != "1.0800000001" {
		t.Fatalf("settled billing = cost=%q ok=%v, want exact amount kept", cost, ok)
	}
	if hits := atomic.LoadInt32(&contentHits); hits != 1 {
		t.Fatalf("content endpoint hits = %d, want exactly 1 (attempted, then timed out)", hits)
	}
}

// TestGateRetrieveLinkSkippedWhenDeadlineExhausted 锁定：外层 context 剩余
// 不足 100ms 余量时取链整体跳过，完全不拨号。
func TestGateRetrieveLinkSkippedWhenDeadlineExhausted(t *testing.T) {
	var contentHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			atomic.AddInt32(&contentHits, 1)
		}
		w.Header().Set("Location", "https://cdn.example.com/v.mp4")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(50*time.Millisecond))
	if got := provider.fetchContentDirectURL(ctx, testKey("s"), "j1"); got != "" {
		t.Fatalf("fetchContentDirectURL() = %q, want skipped empty result", got)
	}
	// 留窗确认没有任何在途拨号落上服务器。
	time.Sleep(150 * time.Millisecond)
	if hits := atomic.LoadInt32(&contentHits); hits != 0 {
		t.Fatalf("content endpoint hits = %d, want 0 (lookup must be skipped)", hits)
	}
}

// TestGateVideoGenerationPreDispatchErrorShape 锁定预分发错误形状：请求
// 转换/序列化失败携带 400 + invalid_request_error + request_not_dispatched；
// 收到响应后的解码失败绝不携带该 code。
func TestGateVideoGenerationPreDispatchErrorShape(t *testing.T) {
	var serverHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&serverHits, 1)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `not-json`)
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	key := testKey("s")

	assertNotDispatchedShape := func(t *testing.T, bifrostErr *schemas.BifrostError, wantMessage string) {
		t.Helper()
		if bifrostErr == nil || !bifrostErr.IsBifrostError {
			t.Fatalf("error = %+v, want bifrost error", bifrostErr)
		}
		if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("StatusCode = %+v, want 400", bifrostErr.StatusCode)
		}
		if bifrostErr.Error == nil {
			t.Fatalf("ErrorField is nil")
		}
		if bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != "invalid_request_error" {
			t.Fatalf("Type = %+v, want invalid_request_error", bifrostErr.Error.Type)
		}
		if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "request_not_dispatched" {
			t.Fatalf("Code = %+v, want request_not_dispatched", bifrostErr.Error.Code)
		}
		if bifrostErr.Error.Message != wantMessage {
			t.Fatalf("Message = %q, want %q", bifrostErr.Error.Message, wantMessage)
		}
	}

	// 请求转换失败（缺 prompt）：未发出任何字节。
	_, bifrostErr := provider.VideoGeneration(testCtx(), key, &schemas.BifrostVideoGenerationRequest{
		Model: "m",
		Input: &schemas.VideoGenerationInput{},
	})
	assertNotDispatchedShape(t, bifrostErr, schemas.ErrRequestBodyConversion)
	if hits := atomic.LoadInt32(&serverHits); hits != 0 {
		t.Fatalf("server hits = %d, want 0 (rejected before dispatch)", hits)
	}

	// 请求序列化失败：Gate 请求体字段全部可序列化，公开路径无法触发，
	// 该分支形状由同一 helper 保证，直接锁定 helper 输出。
	assertNotDispatchedShape(t,
		newRequestNotDispatchedError(schemas.ErrProviderRequestMarshal, fmt.Errorf("boom")),
		schemas.ErrProviderRequestMarshal)

	// 响应解码失败（202 + 非法信封 JSON）：已发网，绝不携带预分发 code，
	// 也不得带 400（下游只对 400 退还预留额度）。
	_, decodeErr := provider.VideoGeneration(testCtx(), key, &schemas.BifrostVideoGenerationRequest{
		Model: "m",
		Input: &schemas.VideoGenerationInput{Prompt: "a cat"},
	})
	if decodeErr == nil {
		t.Fatalf("decode failure must return error")
	}
	if decodeErr.StatusCode != nil && *decodeErr.StatusCode == http.StatusBadRequest {
		t.Fatalf("decode failure StatusCode = %d, must not be 400", *decodeErr.StatusCode)
	}
	if decodeErr.Error != nil && decodeErr.Error.Code != nil && *decodeErr.Error.Code == "request_not_dispatched" {
		t.Fatalf("decode failure must not carry request_not_dispatched code")
	}
}

// TestGateRetrieveLinkNeverFollowsRedirect 锁定：取链绝不跟随 302；返回的
// 是原始 Location，目标 CDN 命中数恒为 0。
func TestGateRetrieveLinkNeverFollowsRedirect(t *testing.T) {
	var cdnHits int32
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&cdnHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer cdn.Close()
	// https 绝对地址才能过 validateDownloadLocation；lookup 绝不得拨它。
	location := cdn.URL + "/signed-direct.mp4"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusFound)
			return
		}
		fmt.Fprint(w, gateRetrieveEnvelope("j1", "completed", "settled", "1.0800000001", true))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	ctx := testCtx()
	resp, bifrostErr := provider.VideoRetrieve(ctx, testKey("s"), &schemas.BifrostVideoRetrieveRequest{ID: "j1:gate"})
	if bifrostErr != nil {
		t.Fatalf("VideoRetrieve() error = %+v", bifrostErr.Error)
	}
	if len(resp.Videos) != 1 || resp.Videos[0].URL == nil || *resp.Videos[0].URL != location {
		t.Fatalf("Videos = %+v, want raw Location %q", resp.Videos, location)
	}
	if hits := atomic.LoadInt32(&cdnHits); hits != 0 {
		t.Fatalf("CDN hits = %d, want 0 (redirect must never be followed)", hits)
	}
}

// TestGateRetrieveLinkHandshakeCancellation 覆盖真实 socket 上的 TLS、
// CONNECT 与 SOCKS5 握手：短预算或显式取消必须关闭已建立的底层连接，
// 不能只让 Client.Do 返回，留下脱离请求 context 的握手 goroutine。
func TestGateRetrieveLinkHandshakeCancellation(t *testing.T) {
	for _, proxyType := range []schemas.ProxyType{schemas.NoProxy, schemas.HTTPProxy, schemas.Socks5Proxy} {
		for _, explicitCancel := range []bool{false, true} {
			name := fmt.Sprintf("%s/cancel=%v", proxyType, explicitCancel)
			t.Run(name, func(t *testing.T) {
				listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}

				config := &schemas.ProviderConfig{
					NetworkConfig: schemas.NetworkConfig{
						BaseURL:                        "https://" + listener.Addr().String(),
						DefaultRequestTimeoutInSeconds: 5,
					},
					ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
				}
				if proxyType != schemas.NoProxy {
					// 目标地址由代理处理，测试不会解析或访问外网。
					config.NetworkConfig.BaseURL = "https://fake-gate.invalid"
					config.ProxyConfig = &schemas.ProxyConfig{
						Type: proxyType,
						URL:  schemas.NewSecretVar(listener.Addr().String()),
					}
				}
				provider, err := NewGateProvider(config, testLogger{})
				if err != nil {
					t.Fatal(err)
				}

				outerTimeout := 600 * time.Millisecond
				if explicitCancel {
					outerTimeout = 3 * time.Second
				}
				ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), outerTimeout)
				defer cancel()
				result := make(chan string, 1)
				go func() {
					result <- provider.fetchContentDirectURL(ctx, testKey("fake-secret"), "j1")
				}()

				conn, err := listener.Accept()
				if err != nil {
					t.Fatalf("handshake connection not opened: %v", err)
				}
				defer conn.Close()
				if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				// 先确认握手字节实际到达，再取消；服务端始终不回握手响应。
				var firstByte [1]byte
				if _, err := io.ReadFull(conn, firstByte[:]); err != nil {
					t.Fatalf("handshake not started: %v", err)
				}
				peerClosed := make(chan error, 1)
				go func() {
					_, err := io.Copy(io.Discard, conn)
					peerClosed <- err
				}()
				if explicitCancel {
					cancel()
				}

				select {
				case got := <-result:
					if got != "" {
						t.Fatalf("URL = %q, want empty on canceled handshake", got)
					}
				case <-time.After(time.Second):
					t.Fatal("lookup did not return within its short budget")
				}
				select {
				case err := <-peerClosed:
					if err != nil {
						t.Fatalf("peer connection was not closed cleanly: %v", err)
					}
				case <-time.After(300 * time.Millisecond):
					t.Fatal("handshake socket remains open after lookup returned")
				}
			})
		}
	}
}

// TestGateRetrieveLinkDNSCancellation 使用仅等待取消的本地 resolver，
// 验证直连与代理拨号的 DNS 都沿用单次取链预算，不会遗留后台解析。
// 临时替换 net.DefaultResolver，因此本测试不可并行。
func TestGateRetrieveLinkDNSCancellation(t *testing.T) {
	for _, useProxy := range []bool{false, true} {
		t.Run(fmt.Sprintf("proxy=%v", useProxy), func(t *testing.T) {
			resolverContexts := make(chan context.Context, 16)
			previousResolver := net.DefaultResolver
			net.DefaultResolver = &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
					select {
					case resolverContexts <- ctx:
					default:
					}
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}
			defer func() { net.DefaultResolver = previousResolver }()

			config := &schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        "https://fake-gate.invalid",
					DefaultRequestTimeoutInSeconds: 5,
				},
				ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
			}
			if useProxy {
				config.ProxyConfig = &schemas.ProxyConfig{
					Type: schemas.HTTPProxy,
					URL:  schemas.NewSecretVar("http://fake-proxy.invalid:8080"),
				}
			}
			provider, err := NewGateProvider(config, testLogger{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 600*time.Millisecond)
			defer cancel()
			if got := provider.fetchContentDirectURL(ctx, testKey("fake-secret"), "j1"); got != "" {
				t.Fatalf("URL = %q, want empty on DNS timeout", got)
			}
			select {
			case resolverCtx := <-resolverContexts:
				select {
				case <-resolverCtx.Done():
				case <-time.After(300 * time.Millisecond):
					t.Fatal("DNS remains active after lookup returned")
				}
			default:
				t.Fatal("lookup did not exercise DNS")
			}
		})
	}
}
