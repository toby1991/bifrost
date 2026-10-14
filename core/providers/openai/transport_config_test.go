package openai

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestNewOpenAIProvider_StaleConnectionRetryConfig(t *testing.T) {
	t.Run("enabled by default", func(t *testing.T) {
		provider := NewOpenAIProvider(&schemas.ProviderConfig{}, nil)

		assertStaleRetryResult(t, provider.client, true)
		assertStaleRetryResult(t, provider.streamingClient, true)
	})

	t.Run("disabled independently of core retries", func(t *testing.T) {
		config := &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				MaxRetries:                  4,
				DisableStaleConnectionRetry: true,
			},
		}
		provider := NewOpenAIProvider(config, nil)

		assertStaleRetryResult(t, provider.client, false)
		assertStaleRetryResult(t, provider.streamingClient, false)
		if config.NetworkConfig.MaxRetries != 4 {
			t.Fatalf("MaxRetries changed: got %d, want 4", config.NetworkConfig.MaxRetries)
		}
	})
}

func assertStaleRetryResult(t *testing.T, client *fasthttp.Client, wantRetry bool) {
	t.Helper()
	if client.RetryIfErr == nil {
		t.Fatal("provider transport should install an explicit retry policy")
	}
	_, retry := client.RetryIfErr(nil, 1, fmt.Errorf("cannot find whitespace"))
	if retry != wantRetry {
		t.Fatalf("stale retry: got %t, want %t", retry, wantRetry)
	}
}
