package llmgwcoupon

import (
	"context"
	"fmt"
	"github.com/maximhq/bifrost/core/schemas"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClient struct {
	claim func(context.Context, string, string, string, uint64) (uint64, *schemas.BifrostError)
	get   func(context.Context, string, string, uint64) (*Claim, *schemas.BifrostError)
	calls atomic.Int64
}

func (c *fakeClient) Claim(ctx context.Context, endpoint, token, pool string, id uint64) (uint64, *schemas.BifrostError) {
	c.calls.Add(1)
	if c.claim != nil {
		return c.claim(ctx, endpoint, token, pool, id)
	}
	return 19, nil
}
func (c *fakeClient) Get(ctx context.Context, endpoint, token string, id uint64) (*Claim, *schemas.BifrostError) {
	c.calls.Add(1)
	if c.get != nil {
		return c.get(ctx, endpoint, token, id)
	}
	return &Claim{ID: id, PoolCode: "BiWei", ExecutionID: 123, CodeValue: "secret+?&/=Case", ClaimedAt: 100}, nil
}
func testContext(client Client, id any) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	if client != nil {
		ctx.SetValue(ClientContextKey, client)
	}
	if id != nil {
		ctx.SetValue(idempotencyContextKey, id)
	}
	return ctx
}
func testProvider(t *testing.T) *LLMGWCouponProvider {
	t.Helper()
	p, err := NewLLMGWCouponProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: "llmgw:8080"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func testKey() schemas.Key { return schemas.Key{Value: *schemas.NewSecretVar("service-secret")} }
func requireNotDispatched(t *testing.T, err *schemas.BifrostError) {
	t.Helper()
	if err == nil || err.Error == nil || err.Error.Code == nil || *err.Error.Code != "request_not_dispatched" || err.AllowFallbacks == nil || *err.AllowFallbacks {
		t.Fatalf("expected non-fallback request_not_dispatched, got %#v", err)
	}
}

func TestCouponConstructor(t *testing.T) {
	for _, target := range []string{"llmgw:8080", "127.0.0.1:9000", "[::1]:8080", " llmgw:8080 "} {
		t.Run(target, func(t *testing.T) {
			p, err := NewLLMGWCouponProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: target}}, nil)
			if err != nil || p.GetProviderKey() != schemas.LLMGWCoupon {
				t.Fatalf("provider=%#v err=%v", p, err)
			}
		})
	}
	for _, target := range []string{"", "http://llmgw:8080", "https://llmgw:8080", "llmgw", ":8080", "llmgw:0", "llmgw:65536", "llmgw:abc", "llmgw:8080/path", "bad host:8080", "bad\thost:8080"} {
		t.Run("reject "+target, func(t *testing.T) {
			if _, err := NewLLMGWCouponProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: target}}, nil); err == nil {
				t.Fatal("accepted invalid gRPC target")
			}
		})
	}
	if _, err := NewLLMGWCouponProvider(nil, nil); err == nil {
		t.Fatal("accepted nil config")
	}
}
func TestCouponSubmitValidatesBeforeRPC(t *testing.T) {
	p := testProvider(t)
	for _, value := range []any{nil, uint64(123), "", "0", "-1", "+1", " 123", "123 ", "execution-123", "18446744073709551616", "1.0"} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			c := &fakeClient{}
			r, err := p.VideoGeneration(testContext(c, value), testKey(), &schemas.BifrostVideoGenerationRequest{Model: "BiWei"})
			requireNotDispatched(t, err)
			if r != nil || c.calls.Load() != 0 {
				t.Fatal("invalid execution reached RPC")
			}
		})
	}
	for _, tc := range []struct {
		name    string
		request *schemas.BifrostVideoGenerationRequest
		client  Client
		key     schemas.Key
	}{
		{"nil request", nil, &fakeClient{}, testKey()},
		{"empty model", &schemas.BifrostVideoGenerationRequest{}, &fakeClient{}, testKey()},
		{"missing client", &schemas.BifrostVideoGenerationRequest{Model: "BiWei"}, nil, testKey()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := p.VideoGeneration(testContext(tc.client, "123"), tc.key, tc.request)
			requireNotDispatched(t, err)
			if r != nil {
				t.Fatal("unexpected result")
			}
		})
	}
}
func TestCouponSubmitReturnsClaimWithoutCode(t *testing.T) {
	p := testProvider(t)
	c := &fakeClient{claim: func(ctx context.Context, endpoint, token, pool string, id uint64) (uint64, *schemas.BifrostError) {
		if endpoint != "llmgw:8080" || token != "service-secret" || pool != "BiWei" || id != 123 {
			t.Fatalf("wrong RPC args: %q %q %q %d", endpoint, token, pool, id)
		}
		return 19, nil
	}}
	ctx := testContext(c, "123")
	r, err := p.VideoGeneration(ctx, testKey(), &schemas.BifrostVideoGenerationRequest{Model: "BiWei"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "19:llmgw-coupon" || r.Model != "BiWei" || r.Status != schemas.VideoStatusInProgress || r.CreatedAt <= 0 || len(r.Videos) != 0 || r.CompletedAt != nil {
		t.Fatalf("unexpected submission: %#v", r)
	}
	if ctx.Value(schemas.BifrostContextKeyExtraHeaders) != nil {
		t.Fatal("internal id became extra headers")
	}
	if c.calls.Load() != 1 {
		t.Fatal("expected exactly one RPC")
	}
}
func TestCouponRPCFailuresKeepClassification(t *testing.T) {
	p := testProvider(t)
	rpcErr := &schemas.BifrostError{StatusCode: schemas.Ptr(503), Error: &schemas.ErrorField{Code: schemas.Ptr("upstream_unknown"), Message: "RPC outcome unknown"}}
	c := &fakeClient{claim: func(context.Context, string, string, string, uint64) (uint64, *schemas.BifrostError) {
		return 0, rpcErr
	}, get: func(context.Context, string, string, uint64) (*Claim, *schemas.BifrostError) { return nil, rpcErr }}
	if _, got := p.VideoGeneration(testContext(c, "123"), testKey(), &schemas.BifrostVideoGenerationRequest{Model: "BiWei"}); got != rpcErr {
		t.Fatal("submit error classification rewritten")
	}
	if _, got := p.VideoRetrieve(testContext(c, "123"), testKey(), &schemas.BifrostVideoRetrieveRequest{ID: "19:llmgw-coupon"}); got != rpcErr {
		t.Fatal("retrieve error classification rewritten")
	}
	c.claim = func(context.Context, string, string, string, uint64) (uint64, *schemas.BifrostError) { return 0, nil }
	if _, err := p.VideoGeneration(testContext(c, "123"), testKey(), &schemas.BifrostVideoGenerationRequest{Model: "BiWei"}); err == nil || (err.Error.Code != nil && *err.Error.Code == "request_not_dispatched") {
		t.Fatal("zero claim id treated as safe undispatched")
	}
}
func TestCouponRetrieveRawCode(t *testing.T) {
	p := testProvider(t)
	for _, id := range []any{nil, "123"} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			c := &fakeClient{get: func(ctx context.Context, endpoint, token string, id uint64) (*Claim, *schemas.BifrostError) {
				if endpoint != "llmgw:8080" || token != "service-secret" || id != 19 {
					t.Fatal("wrong Get args")
				}
				return &Claim{ID: 19, PoolCode: "BiWei", ExecutionID: 123, CodeValue: "secret+?&/=Case", ClaimedAt: 100}, nil
			}}
			r, err := p.VideoRetrieve(testContext(c, id), testKey(), &schemas.BifrostVideoRetrieveRequest{ID: "19:llmgw-coupon"})
			if err != nil {
				t.Fatal(err)
			}
			if r.ID != "19:llmgw-coupon" || r.Model != "BiWei" || r.Status != schemas.VideoStatusCompleted || r.CreatedAt != 100 || r.CompletedAt == nil || *r.CompletedAt != 100 {
				t.Fatalf("bad response %#v", r)
			}
			want := []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL, URL: schemas.Ptr("secret+?&/=Case"), ContentType: "text/plain"}}
			if !reflect.DeepEqual(r.Videos, want) {
				t.Fatalf("raw coupon changed: %#v", r.Videos)
			}
			if c.calls.Load() != 1 {
				t.Fatal("retrieve made additional RPCs")
			}
		})
	}
}
func TestCouponRetrieveValidatesBeforeRPC(t *testing.T) {
	p := testProvider(t)
	for _, id := range []string{"", "0", "-1", "abc", "19:gate", "18446744073709551616"} {
		t.Run(id, func(t *testing.T) {
			c := &fakeClient{}
			_, err := p.VideoRetrieve(testContext(c, "123"), testKey(), &schemas.BifrostVideoRetrieveRequest{ID: id})
			requireNotDispatched(t, err)
			if c.calls.Load() != 0 {
				t.Fatal("invalid claim reached RPC")
			}
		})
	}
	for _, id := range []any{uint64(123), "0", "execution-123", "18446744073709551616"} {
		c := &fakeClient{}
		_, err := p.VideoRetrieve(testContext(c, id), testKey(), &schemas.BifrostVideoRetrieveRequest{ID: "19"})
		requireNotDispatched(t, err)
		if c.calls.Load() != 0 {
			t.Fatal("invalid execution reached RPC")
		}
	}
}
func TestCouponRetrieveRejectsInconsistentClaim(t *testing.T) {
	p := testProvider(t)
	cases := map[string]func(*Claim){"wrong claim": func(c *Claim) { c.ID = 20 }, "wrong execution": func(c *Claim) { c.ExecutionID = 124 }, "zero execution": func(c *Claim) { c.ExecutionID = 0 }, "empty pool": func(c *Claim) { c.PoolCode = "" }, "empty code": func(c *Claim) { c.CodeValue = "" }, "missing time": func(c *Claim) { c.ClaimedAt = 0 }}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			claim := &Claim{ID: 19, ExecutionID: 123, PoolCode: "BiWei", CodeValue: "secret", ClaimedAt: 100}
			mutate(claim)
			c := &fakeClient{get: func(context.Context, string, string, uint64) (*Claim, *schemas.BifrostError) { return claim, nil }}
			r, err := p.VideoRetrieve(testContext(c, "123"), testKey(), &schemas.BifrostVideoRetrieveRequest{ID: "19"})
			if err == nil || r != nil {
				t.Fatal("invalid claim delivered")
			}
		})
	}
}
func TestCouponConcurrentSubmissionsIsolated(t *testing.T) {
	p := testProvider(t)
	c := &fakeClient{claim: func(_ context.Context, endpoint, token, pool string, id uint64) (uint64, *schemas.BifrostError) {
		return id, nil
	}}
	var wg sync.WaitGroup
	for id := uint64(1); id <= 32; id++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := p.VideoGeneration(testContext(c, fmt.Sprint(id)), testKey(), &schemas.BifrostVideoGenerationRequest{Model: "BiWei"})
			if err != nil || r.ID != fmt.Sprintf("%d:llmgw-coupon", id) {
				t.Errorf("cross request result for %d: %#v %v", id, r, err)
			}
		}()
	}
	wg.Wait()
	if c.calls.Load() != 32 {
		t.Fatalf("calls=%d", c.calls.Load())
	}
}
func TestCouponUnsupportedOperations(t *testing.T) {
	p := testProvider(t)
	c := &fakeClient{}
	ctx := testContext(c, "123")
	if _, err := p.VideoDownload(ctx, testKey(), &schemas.BifrostVideoDownloadRequest{}); err == nil {
		t.Fatal("download allowed")
	}
	if _, err := p.ChatCompletion(ctx, testKey(), &schemas.BifrostChatRequest{}); err == nil {
		t.Fatal("chat allowed")
	}
	if _, err := p.Responses(ctx, testKey(), &schemas.BifrostResponsesRequest{}); err == nil {
		t.Fatal("responses allowed")
	}
	if c.calls.Load() != 0 {
		t.Fatal("unsupported RPC invoked")
	}
}
func BenchmarkCouponSubmit(b *testing.B) {
	p, _ := NewLLMGWCouponProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: "llmgw:8080"}}, nil)
	ctx := testContext(&fakeClient{}, "123")
	key := testKey()
	request := &schemas.BifrostVideoGenerationRequest{Model: "BiWei"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := p.VideoGeneration(ctx, key, request); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCouponRPCTimeoutContext(t *testing.T) {
	p, err := NewLLMGWCouponProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: "llmgw:8080", DefaultRequestTimeoutInSeconds: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second || time.Until(deadline) <= 0 {
			t.Fatal("configured RPC deadline missing")
		}
	}
	c := &fakeClient{claim: func(ctx context.Context, _ string, _ string, _ string, _ uint64) (uint64, *schemas.BifrostError) {
		check(ctx)
		return 19, nil
	}, get: func(ctx context.Context, _ string, _ string, _ uint64) (*Claim, *schemas.BifrostError) {
		check(ctx)
		return &Claim{ID: 19, PoolCode: "BiWei", ExecutionID: 123, CodeValue: "secret", ClaimedAt: 100}, nil
	}}
	ctx := testContext(c, "123")
	if _, err := p.VideoGeneration(ctx, testKey(), &schemas.BifrostVideoGenerationRequest{Model: "BiWei"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.VideoRetrieve(ctx, testKey(), &schemas.BifrostVideoRetrieveRequest{ID: "19"}); err != nil {
		t.Fatal(err)
	}
}

// 使用现有 Core IsKeyLess 分支时传入零值 Key，不需要占位凭证。
func TestCouponZeroKeyClaimAndRetrieve(t *testing.T) {
	p := testProvider(t)
	c := &fakeClient{claim: func(_ context.Context, endpoint, token, pool string, id uint64) (uint64, *schemas.BifrostError) {
		if endpoint != "llmgw:8080" || token != "" || pool != "BiWei" || id != 123 {
			t.Fatalf("wrong keyless Claim arguments: %q %q %q %d", endpoint, token, pool, id)
		}
		return 19, nil
	}, get: func(_ context.Context, endpoint, token string, id uint64) (*Claim, *schemas.BifrostError) {
		if endpoint != "llmgw:8080" || token != "" || id != 19 {
			t.Fatalf("wrong keyless Get arguments: %q %q %d", endpoint, token, id)
		}
		return &Claim{ID: 19, PoolCode: "BiWei", ExecutionID: 123, CodeValue: "original-code", ClaimedAt: 100}, nil
	}}
	submitted, err := p.VideoGeneration(testContext(c, "123"), schemas.Key{}, &schemas.BifrostVideoGenerationRequest{Model: "BiWei"})
	if err != nil || submitted == nil || submitted.ID != "19:llmgw-coupon" {
		t.Fatalf("keyless claim: response=%#v err=%v", submitted, err)
	}
	retrieved, err := p.VideoRetrieve(testContext(c, "123"), schemas.Key{}, &schemas.BifrostVideoRetrieveRequest{ID: submitted.ID})
	if err != nil || retrieved == nil || len(retrieved.Videos) != 1 || retrieved.Videos[0].URL == nil || *retrieved.Videos[0].URL != "original-code" {
		t.Fatalf("keyless retrieve: response=%#v err=%v", retrieved, err)
	}
	if c.calls.Load() != 2 {
		t.Fatalf("calls=%d", c.calls.Load())
	}
}

func TestCouponZeroKeyStillRequiresRPCAdapter(t *testing.T) {
	p := testProvider(t)
	submitted, err := p.VideoGeneration(testContext(nil, "123"), schemas.Key{}, &schemas.BifrostVideoGenerationRequest{Model: "BiWei"})
	requireNotDispatched(t, err)
	if submitted != nil {
		t.Fatal("missing adapter returned submission")
	}
	retrieved, err := p.VideoRetrieve(testContext(nil, "123"), schemas.Key{}, &schemas.BifrostVideoRetrieveRequest{ID: "19"})
	requireNotDispatched(t, err)
	if retrieved != nil {
		t.Fatal("missing adapter returned coupon")
	}
}
