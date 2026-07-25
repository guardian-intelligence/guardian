package transfer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

const testSecret = "transfer-secret"

func testServer(t *testing.T, store zvol.TransferStore) *httptest.Server {
	t.Helper()
	server, err := New(Config{Store: store, Secret: []byte(testSecret)})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func get(t *testing.T, server *httptest.Server, path, secret string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func sealFakeGeneration(t *testing.T, store *zvol.Fake, generation zvol.GenerationID) {
	t.Helper()
	ctx := context.Background()
	assignment := zvol.AssignmentID("seed-" + string(generation))
	if _, err := store.EnsureWorkspace(ctx, assignment, "", 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureTool(ctx, assignment, "", 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureProcess(ctx, assignment, "", 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SealSet(ctx, assignment, generation); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsMissingAndWrongCredential(t *testing.T) {
	store := zvol.NewFake()
	sealFakeGeneration(t, store, "gen-auth")
	server := testServer(t, store)
	if response := get(t, server, GenerationPathPrefix+"gen-auth", ""); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing credential status = %d", response.StatusCode)
	}
	if response := get(t, server, GenerationPathPrefix+"gen-auth", "wrong-secret"); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong credential status = %d", response.StatusCode)
	}
}

func TestRejectsHostileGenerationNames(t *testing.T) {
	store := zvol.NewFake()
	sealFakeGeneration(t, store, "gen-safe")
	server := testServer(t, store)
	for _, hostile := range []struct {
		path string
		want int
	}{
		// Traversal out of the generation namespace.
		{GenerationPathPrefix + "gen-safe%2F..%2F..%2Fimages", http.StatusBadRequest},
		{GenerationPathPrefix + "a%2Fb", http.StatusBadRequest},
		// Snapshot-name smuggling.
		{GenerationPathPrefix + "gen-safe%40sealed", http.StatusBadRequest},
		{GenerationPathPrefix + "gen-safe%40seal-x", http.StatusBadRequest},
		// Absolute dataset path.
		{GenerationPathPrefix + "%2Ftank%2Fpostflight%2Fgen%2Fgen-safe", http.StatusBadRequest},
		// Leading-dash and leading-dot names never splice into argv.
		{GenerationPathPrefix + "-oProxyCommand", http.StatusBadRequest},
		{GenerationPathPrefix + ".hidden", http.StatusBadRequest},
		// The golden-image dataset lives outside the generation namespace:
		// name-valid but not a sealed generation.
		{GenerationPathPrefix + "images", http.StatusNotFound},
		// Hostile query parameters.
		{GenerationPathPrefix + "gen-safe?tree=..%2Fws", http.StatusBadRequest},
		{GenerationPathPrefix + "gen-safe?from=a%2Fb", http.StatusBadRequest},
		{GenerationPathPrefix + "gen-safe?from=base%40sealed", http.StatusBadRequest},
	} {
		response := get(t, server, hostile.path, testSecret)
		if response.StatusCode != hostile.want {
			t.Errorf("%s status = %d, want %d", hostile.path, response.StatusCode, hostile.want)
		}
	}
}

func TestUnknownGenerationIsNotFound(t *testing.T) {
	server := testServer(t, zvol.NewFake())
	if response := get(t, server, GenerationPathPrefix+"gen-missing", testSecret); response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown generation status = %d", response.StatusCode)
	}
}

func TestStreamsGenerationIntoPeerStore(t *testing.T) {
	source := zvol.NewFake()
	sealFakeGeneration(t, source, "gen-stream")
	server := testServer(t, source)
	destination := zvol.NewFake()
	ctx := context.Background()
	for _, tree := range zvol.TransferTrees {
		response := get(t, server, GenerationPathPrefix+"gen-stream?tree="+string(tree), testSecret)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s stream status = %d", tree, response.StatusCode)
		}
		if got := response.Header.Get(IncrementalHeader); got != "false" {
			t.Fatalf("%s incremental header = %q", tree, got)
		}
		if err := destination.ReceiveGeneration(ctx, "gen-stream", tree, response.Body); err != nil {
			t.Fatalf("receive %s: %v", tree, err)
		}
	}
	resident, cached, err := destination.GenerationState(ctx, "gen-stream")
	if err != nil || resident || !cached {
		t.Fatalf("destination state resident=%t cached=%t err=%v", resident, cached, err)
	}
	// The transfer cache is not residency: inventory must stay empty.
	generations, _, err := destination.Inventory(ctx)
	if err != nil || len(generations) != 0 {
		t.Fatalf("inventory after receive = %+v, %v", generations, err)
	}
	// A workspace clone comes up warm from the cache.
	volume, err := destination.EnsureWorkspace(ctx, "assignment-warm", "gen-stream", 1<<20)
	if err != nil || volume.Source != "gen-stream" {
		t.Fatalf("clone from cache = %+v, %v", volume, err)
	}
}

func TestIncrementalStreamRequiresDirectParent(t *testing.T) {
	source := zvol.NewFake()
	sealFakeGeneration(t, source, "gen-parent")
	ctx := context.Background()
	if _, err := source.EnsureWorkspace(ctx, "seed-child", "gen-parent", 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := source.EnsureTool(ctx, "seed-child", "gen-parent", 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := source.EnsureProcess(ctx, "seed-child", "gen-parent", 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SealSet(ctx, "seed-child", "gen-child"); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, source)

	response := get(t, server, GenerationPathPrefix+"gen-child?from=gen-parent", testSecret)
	if response.StatusCode != http.StatusOK || response.Header.Get(IncrementalHeader) != "true" {
		t.Fatalf("direct-parent stream status=%d incremental=%q", response.StatusCode, response.Header.Get(IncrementalHeader))
	}
	// A base that is not the direct ZFS parent degrades to a full stream.
	response = get(t, server, GenerationPathPrefix+"gen-child?from=gen-unrelated", testSecret)
	if response.StatusCode != http.StatusOK || response.Header.Get(IncrementalHeader) != "false" {
		t.Fatalf("unrelated-base stream status=%d incremental=%q", response.StatusCode, response.Header.Get(IncrementalHeader))
	}
}

// blockingStore parks Send until released so concurrency limits are
// observable.
type blockingStore struct {
	zvol.TransferStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingStore) Send(ctx context.Context, plan zvol.SendPlan, w io.Writer) (int64, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.TransferStore.Send(ctx, plan, w)
}

func TestConcurrencyCapAndSingleflightReturn429(t *testing.T) {
	source := zvol.NewFake()
	sealFakeGeneration(t, source, "gen-busy")
	sealFakeGeneration(t, source, "gen-idle")
	blocking := &blockingStore{TransferStore: source, started: make(chan struct{}), release: make(chan struct{})}
	server, err := New(Config{Store: blocking, Secret: []byte(testSecret), MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	first := make(chan int, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, httpServer.URL+GenerationPathPrefix+"gen-busy", nil)
		request.Header.Set("Authorization", "Bearer "+testSecret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			first <- 0
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		first <- response.StatusCode
	}()
	<-blocking.started

	// Same generation and tree while in flight: deduplicated with 429.
	response := get(t, httpServer, GenerationPathPrefix+"gen-busy", testSecret)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("singleflight status = %d", response.StatusCode)
	}
	if response.Header.Get("Retry-After") == "" {
		t.Fatal("throttled response carries no Retry-After")
	}

	close(blocking.release)
	if status := <-first; status != http.StatusOK {
		t.Fatalf("blocked stream finished with %d", status)
	}
}

func TestConcurrencySlotsExhaustedReturn429(t *testing.T) {
	source := zvol.NewFake()
	sealFakeGeneration(t, source, "gen-slot")
	sealFakeGeneration(t, source, "gen-other")
	blocking := &blockingStore{TransferStore: source, started: make(chan struct{}), release: make(chan struct{})}
	server, err := New(Config{Store: blocking, Secret: []byte(testSecret), MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	first := make(chan int, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, httpServer.URL+GenerationPathPrefix+"gen-slot", nil)
		request.Header.Set("Authorization", "Bearer "+testSecret)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			first <- 0
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		first <- response.StatusCode
	}()
	<-blocking.started

	// A different generation still bounces off the concurrency cap.
	response := get(t, httpServer, GenerationPathPrefix+"gen-other", testSecret)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("capped status = %d", response.StatusCode)
	}
	close(blocking.release)
	if status := <-first; status != http.StatusOK {
		t.Fatalf("blocked stream finished with %d", status)
	}
}

func TestOnlyGetIsAllowed(t *testing.T) {
	server := testServer(t, zvol.NewFake())
	request, err := http.NewRequest(http.MethodPost, server.URL+GenerationPathPrefix+"gen-x", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testSecret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", response.StatusCode)
	}
}
