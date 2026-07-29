package bifrost

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestDisabledStaleConnectionRetryDoesNotDisableCoreRetries(t *testing.T) {
	config := createTestConfig(2, time.Millisecond, time.Millisecond)
	config.NetworkConfig.DisableStaleConnectionRetry = true

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
	callCount := 0
	handler := func(_ schemas.Key) (string, *schemas.BifrostError) {
		callCount++
		return "", createBifrostError("rate limit exceeded", Ptr(429), nil, false)
	}

	_, err := executeRequestWithRetries(
		ctx,
		config,
		handler,
		nil,
		schemas.ChatCompletionRequest,
		schemas.OpenAI,
		"gpt-4",
		nil,
		NewDefaultLogger(schemas.LogLevelError),
	)
	if err == nil {
		t.Fatal("expected terminal error after core retries")
	}
	if callCount != 3 {
		t.Fatalf("core attempts = %d, want 3 (initial request plus two retries)", callCount)
	}
}
