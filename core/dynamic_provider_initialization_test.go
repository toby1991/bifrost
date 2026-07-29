package bifrost

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

type countingConfigAccount struct {
	*MockAccount
	configReads atomic.Int32
}

func (a *countingConfigAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	a.configReads.Add(1)
	return a.MockAccount.GetConfigForProvider(provider)
}

func TestDynamicProviderInitialization_DefaultBehavior(t *testing.T) {
	account := NewMockAccount()
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer client.Shutdown()

	account.AddProviderWithBaseURL(schemas.Ollama, 1, 1, "http://localhost:11434")
	if _, err := client.getProviderQueue(schemas.Ollama); err != nil {
		t.Fatalf("default configuration should allow lazy initialization: %v", err)
	}
	if provider := client.getProviderByKey(schemas.Ollama); provider == nil {
		t.Fatal("provider should be initialized lazily by default")
	}
}

func TestDynamicProviderInitialization_Disabled(t *testing.T) {
	account := &countingConfigAccount{MockAccount: NewMockAccount()}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:                              account,
		Logger:                               NewDefaultLogger(schemas.LogLevelError),
		DisableDynamicProviderInitialization: true,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer client.Shutdown()

	account.AddProviderWithBaseURL(schemas.Ollama, 1, 1, "http://localhost:11434")
	if _, err := client.getProviderQueue(schemas.Ollama); err == nil ||
		!strings.Contains(err.Error(), "dynamic provider initialization is disabled") {
		t.Fatalf("getProviderQueue error = %v, want dynamic-initialization-disabled error", err)
	}
	if provider := client.getProviderByKey(schemas.Ollama); provider != nil {
		t.Fatalf("getProviderByKey = %#v, want nil while dynamic initialization is disabled", provider)
	}
	if _, exists := client.requestQueues.Load(schemas.Ollama); exists {
		t.Fatal("disabled lazy initialization must not publish a request queue")
	}
	if reads := account.configReads.Load(); reads != 0 {
		t.Fatalf("disabled lazy initialization read provider config %d time(s), want 0", reads)
	}
}

func TestDynamicProviderInitialization_DisabledKeepsStartupProviders(t *testing.T) {
	const providerKey schemas.ModelProvider = "startup-openai"

	account := NewMockAccount()
	addCustomOpenAIProvider(account, providerKey)
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:                              account,
		Logger:                               NewDefaultLogger(schemas.LogLevelError),
		DisableDynamicProviderInitialization: true,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer client.Shutdown()

	if provider := client.getProviderByKey(providerKey); provider == nil {
		t.Fatal("provider configured during Init should be available")
	}
	if _, err := client.getProviderQueue(providerKey); err != nil {
		t.Fatalf("provider configured during Init should have a queue: %v", err)
	}
}

func TestDynamicProviderInitialization_DisabledAllowsExplicitUpdate(t *testing.T) {
	const providerKey schemas.ModelProvider = "custom-openai"

	account := NewMockAccount()
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:                              account,
		Logger:                               NewDefaultLogger(schemas.LogLevelError),
		DisableDynamicProviderInitialization: true,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer client.Shutdown()

	addCustomOpenAIProvider(account, providerKey)

	if err := client.UpdateProvider(providerKey); err != nil {
		t.Fatalf("UpdateProvider should initialize an explicitly configured provider: %v", err)
	}
	if _, err := client.getProviderQueue(providerKey); err != nil {
		t.Fatalf("explicitly initialized provider queue should remain available: %v", err)
	}
	if provider := client.getProviderByKey(providerKey); provider == nil {
		t.Fatal("explicitly initialized provider should be available")
	} else if provider.GetProviderKey() != providerKey {
		t.Fatalf("provider key = %q, want %q", provider.GetProviderKey(), providerKey)
	}

	if err := client.RemoveProvider(providerKey); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}
	if _, err := client.getProviderQueue(providerKey); err == nil ||
		!strings.Contains(err.Error(), "dynamic provider initialization is disabled") {
		t.Fatalf("removed provider was lazily recreated: %v", err)
	}
	if provider := client.getProviderByKey(providerKey); provider != nil {
		t.Fatalf("removed provider was lazily recreated: %#v", provider)
	}

	if err := client.UpdateProvider(providerKey); err != nil {
		t.Fatalf("UpdateProvider should explicitly restore a removed provider: %v", err)
	}
	if provider := client.getProviderByKey(providerKey); provider == nil {
		t.Fatal("explicitly restored provider should be available")
	}
}

func addCustomOpenAIProvider(account *MockAccount, providerKey schemas.ModelProvider) {
	account.mu.Lock()
	account.configs[providerKey] = &schemas.ProviderConfig{
		NetworkConfig: schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 1,
			BufferSize:  1,
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
		},
	}
	account.mu.Unlock()
}
