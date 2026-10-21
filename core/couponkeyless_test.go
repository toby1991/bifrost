package bifrost

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/llmgwcoupon"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestProviderRequiresKeyCoupon(t *testing.T) {
	for _, tc := range []struct {
		name   string
		base   schemas.ModelProvider
		custom *schemas.CustomProviderConfig
		want   bool
	}{
		{"builtin coupon", schemas.LLMGWCoupon, nil, false},
		{"custom coupon", schemas.LLMGWCoupon, &schemas.CustomProviderConfig{BaseProviderType: schemas.LLMGWCoupon}, false},
		{"explicit keyless coupon", schemas.LLMGWCoupon, &schemas.CustomProviderConfig{BaseProviderType: schemas.LLMGWCoupon, IsKeyLess: true}, false},
		{"builtin openai unchanged", schemas.OpenAI, nil, true},
		{"builtin gate unchanged", schemas.Gate, nil, true},
		{"custom openai unchanged", schemas.OpenAI, &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI}, true},
		{"keyless openai unchanged", schemas.OpenAI, &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true}, false},
		{"bedrock exception unchanged", schemas.Bedrock, &schemas.CustomProviderConfig{BaseProviderType: schemas.Bedrock, IsKeyLess: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerRequiresKey(tc.base, tc.custom); got != tc.want {
				t.Fatalf("providerRequiresKey=%v want %v", got, tc.want)
			}
		})
	}
}

type keylessCouponClient struct {
	claims atomic.Int64
	gets   atomic.Int64
}

func (c *keylessCouponClient) Claim(_ context.Context, endpoint, token, pool string, executionID uint64) (uint64, *schemas.BifrostError) {
	c.claims.Add(1)
	if endpoint != "llmgw:8080" || token != "" || pool != "BiWei" || executionID != 123 {
		return 0, newBifrostErrorFromMsg("incorrect keyless Claim arguments")
	}
	return 19, nil
}

func (c *keylessCouponClient) Get(_ context.Context, endpoint, token string, claimID uint64) (*llmgwcoupon.Claim, *schemas.BifrostError) {
	c.gets.Add(1)
	if endpoint != "llmgw:8080" || token != "" || claimID != 19 {
		return nil, newBifrostErrorFromMsg("incorrect keyless Get arguments")
	}
	return &llmgwcoupon.Claim{ID: 19, PoolCode: "BiWei", ExecutionID: 123, CodeValue: "original-code", ClaimedAt: 100}, nil
}

// 通过真实 worker 入口验证无需凭证，避免只测 Provider 而漏掉 Core 选 Key 阶段。
func TestCouponKeylessWorkerDispatch(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := schemas.LLMGWCoupon
		if custom {
			name = "coupon-instance"
		}
		t.Run(string(name), func(t *testing.T) {
			account := NewMockAccount()
			account.AddProviderWithBaseURL(name, 1, 2, "llmgw:8080")
			account.SetKeysForProvider(name, nil)
			account.configs[name].NetworkConfig.MaxRetries = 0
			if custom {
				account.SetCustomProviderConfig(name, &schemas.CustomProviderConfig{BaseProviderType: schemas.LLMGWCoupon})
			}
			client, err := Init(context.Background(), schemas.BifrostConfig{Account: account, Logger: NewNoOpLogger()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Shutdown)
			adapter := &keylessCouponClient{}
			newContext := func() *schemas.BifrostContext {
				ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
				ctx.SetValue(llmgwcoupon.ClientContextKey, adapter)
				ctx.SetValue(schemas.BifrostContextKey("Idempotency-Key"), "123")
				t.Cleanup(ctx.Cancel)
				return ctx
			}
			lookups := account.KeyLookupCount(name)
			submitted, bifrostErr := client.VideoGenerationRequest(newContext(), &schemas.BifrostVideoGenerationRequest{
				Provider: name, Model: "BiWei", Input: &schemas.VideoGenerationInput{Prompt: "claim_coupon"},
			})
			if bifrostErr != nil || submitted == nil || submitted.ID != "19:"+string(name) {
				t.Fatalf("keyless worker claim: response=%#v err=%v", submitted, bifrostErr)
			}
			retrieved, bifrostErr := client.VideoRetrieveRequest(newContext(), &schemas.BifrostVideoRetrieveRequest{Provider: name, ID: submitted.ID})
			if bifrostErr != nil || retrieved == nil || len(retrieved.Videos) != 1 || retrieved.Videos[0].URL == nil || *retrieved.Videos[0].URL != "original-code" {
				t.Fatalf("keyless worker retrieve: response=%#v err=%v", retrieved, bifrostErr)
			}
			if got := account.KeyLookupCount(name); got != lookups {
				t.Fatalf("keyless dispatch performed %d credential lookups", got-lookups)
			}
			if adapter.claims.Load() != 1 || adapter.gets.Load() != 1 {
				t.Fatalf("RPC calls: claims=%d gets=%d", adapter.claims.Load(), adapter.gets.Load())
			}
		})
	}
}
