package bifrost

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestProviderConstructionFailureDoesNotPublishRuntimeState(t *testing.T) {
	account := NewMockAccount()
	providerKey := schemas.ModelProvider("broken-provider")

	account.mu.Lock()
	account.configs[providerKey] = &schemas.ProviderConfig{
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 1,
			BufferSize:  1,
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.ModelProvider("unsupported-base-provider"),
		},
	}
	account.mu.Unlock()

	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer client.Shutdown()

	if _, ok := client.requestQueues.Load(providerKey); ok {
		t.Fatal("failed provider published a request queue")
	}
	if _, ok := client.waitGroups.Load(providerKey); ok {
		t.Fatal("failed provider published a wait group")
	}
	if provider := client.getProviderByKey(providerKey); provider != nil {
		t.Fatalf("getProviderByKey() = %#v, want nil", provider)
	}
	if _, ok := client.requestQueues.Load(providerKey); ok {
		t.Fatal("lazy provider failure published a request queue")
	}
}

func TestShutdownRejectsProviderLifecycleChanges(t *testing.T) {
	account := NewMockAccount()
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	account.AddProvider(schemas.OpenAI, 1, 1)
	client.Shutdown()

	if _, err := client.getProviderQueue(schemas.OpenAI); err == nil ||
		!strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("getProviderQueue() error = %v, want shutdown error", err)
	}
	if provider := client.getProviderByKey(schemas.OpenAI); provider != nil {
		t.Fatalf("getProviderByKey() = %#v after shutdown, want nil", provider)
	}
	if err := client.RemoveProvider(schemas.OpenAI); err == nil ||
		!strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("RemoveProvider() error = %v, want shutdown error", err)
	}
	if _, ok := client.requestQueues.Load(schemas.OpenAI); ok {
		t.Fatal("provider queue was created after shutdown")
	}
}

type blockingConfigAccount struct {
	*MockAccount
	provider schemas.ModelProvider
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (a *blockingConfigAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if provider == a.provider {
		a.once.Do(func() {
			close(a.entered)
		})
		<-a.release
	}
	return a.MockAccount.GetConfigForProvider(provider)
}

func TestShutdownWaitsForLazyProviderPublication(t *testing.T) {
	const shutdownBlockWindow = 100 * time.Millisecond

	account := &blockingConfigAccount{
		MockAccount: NewMockAccount(),
		provider:    schemas.OpenAI,
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	account.AddProvider(schemas.OpenAI, 1, 1)
	releaseProvider := sync.OnceFunc(func() {
		close(account.release)
	})
	defer releaseProvider()

	queueResult := make(chan error, 1)
	go func() {
		_, err := client.getProviderQueue(schemas.OpenAI)
		queueResult <- err
	}()

	select {
	case <-account.entered:
	case <-time.After(time.Second):
		t.Fatal("lazy provider initialization did not request its config")
	}

	shutdownStarted := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		close(shutdownStarted)
		client.Shutdown()
		close(shutdownDone)
	}()
	<-shutdownStarted

	select {
	case <-shutdownDone:
		releaseProvider()
		<-queueResult
		client.Shutdown()
		t.Fatal("Shutdown() returned before lazy provider publication completed")
	case <-time.After(shutdownBlockWindow):
	}

	releaseProvider()
	if err := <-queueResult; err != nil {
		t.Fatalf("getProviderQueue() error = %v", err)
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown() did not finish after lazy provider publication")
	}

	value, ok := client.requestQueues.Load(schemas.OpenAI)
	if !ok {
		t.Fatal("published provider queue not found")
	}
	if pq := value.(*ProviderQueue); !pq.isClosing() {
		t.Fatal("provider queue remained open after Shutdown()")
	}
	if _, err := client.getProviderQueue(schemas.OpenAI); err == nil {
		t.Fatal("getProviderQueue() succeeded after Shutdown()")
	}
}
