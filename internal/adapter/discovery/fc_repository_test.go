package discovery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/thushan/olla/internal/adapter/discovery"
	"github.com/thushan/olla/internal/adapter/registry"
	"github.com/thushan/olla/internal/adapter/registry/profile"
	"github.com/thushan/olla/internal/config"
	"github.com/thushan/olla/internal/logger"
)

// FCRegistryEntry mirrors the Flight Controller RegistryEntry type.
type FCRegistryEntry struct {
	Host   string        `json:"host"`
	Models []FCModelSpec `json:"models"`
}

type FCModelSpec struct {
	Name                    string   `json:"name"`
	Framework               string   `json:"framework,omitempty"`
	OllaEndpointType        string   `json:"ollaEndpointType,omitempty"`
	EndpointType            string   `json:"endpointType,omitempty"`
	HealthCheckURL          string   `json:"healthCheckURL,omitempty"`
	ModelURL                string   `json:"modelURL,omitempty"`
	CircuitBreakerTimeout   string   `json:"circuitBreakerTimeout,omitempty"`
	Port                    int      `json:"port"`
	Priority                int      `json:"priority,omitempty"`
	CircuitBreakerThreshold int      `json:"circuitBreakerThreshold,omitempty"`
	Capabilities            []string `json:"capabilities,omitempty"`
	ModelName               string   `json:"modelName,omitempty"`
	LoadedModel             string   `json:"loadedModel,omitempty"`
}

func TestFCEndpointRepository_PollMixedPayloadSkipsUnrecognizedAndLoadsEmbeddings(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "oblivion.petersimmons.com",
			Models: []FCModelSpec{
				{
					Name:             "qwen3-32b-vllm",
					Framework:        "vllm",
					OllaEndpointType: "openai-compatible",
					Capabilities:     []string{"chat"},
					Port:             8000,
				},
				{
					Name:         "bad-endpoint",
					Framework:    "mystery-framework",
					Capabilities: []string{"chat"},
					Port:         8010,
				},
			},
		},
		{
			Host: "precision.petersimmons.com",
			Models: []FCModelSpec{
				{
					Name:             "bge-m3-infinity",
					Framework:        "infinity",
					OllaEndpointType: "openai-compatible",
					Capabilities:     []string{"embeddings"},
					Port:             8005,
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	poller := discovery.NewFCDiscoveryPoller(repo, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v", err)
	}

	all, err := repo.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll() returned error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 loaded endpoints (1 skipped), got %d", len(all))
	}

	embeddingsCount := 0
	openAICompatTypeCount := 0
	for _, ep := range all {
		if ep.Type == "openai-compatible" {
			openAICompatTypeCount++
		}
		for _, capability := range ep.Capabilities {
			if capability == "embeddings" {
				embeddingsCount++
			}
		}
	}
	if embeddingsCount == 0 {
		t.Fatalf("expected at least one embeddings-capable endpoint in loaded set")
	}
	if openAICompatTypeCount == 0 {
		t.Fatalf("expected at least one loaded endpoint with Type openai-compatible")
	}
}

func newTestLogger() logger.StyledLogger {
	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	_ = cleanup // test logger; cleanup deferred but not critical here
	return logger.NewPlainStyledLogger(log)
}

// TestFCEndpointRepository_PollConvertsRegistryToEndpoints verifies that the FC
// discovery poller transforms Flight Controller /registry entries into Olla endpoints.
func TestFCEndpointRepository_PollConvertsRegistryToEndpoints(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "oblivion.petersimmons.com",
			Models: []FCModelSpec{
				{Name: "qwen3-32b-vllm", Port: 8000},
				{Name: "bge-m3-infinity", Port: 8003},
			},
		},
		{
			Host: "precision.petersimmons.com",
			Models: []FCModelSpec{
				{Name: "bge-m3-infinity", Port: 8005},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	poller := discovery.NewFCDiscoveryPoller(repo, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v", err)
	}

	all, err := repo.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll() returned error: %v", err)
	}

	// 3 total endpoints: 2 from oblivion + 1 from precision
	if len(all) != 3 {
		t.Errorf("expected 3 endpoints, got %d", len(all))
		for _, e := range all {
			t.Logf("  endpoint: name=%q url=%q", e.Name, e.URLString)
		}
	}

	// Verify a specific endpoint URL was generated correctly
	wantURL := "http://oblivion.petersimmons.com:8000"
	found := false
	for _, e := range all {
		if e.URLString == wantURL {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected endpoint with URL %q but not found", wantURL)
	}
}

// TestFCEndpointRepository_PollRemovesStaleEndpoints verifies that when FC registry
// shrinks (host goes offline), polling removes the stale endpoints from Olla's rotation.
// This is the core acceptance criterion for instinct#12.
func TestFCEndpointRepository_PollRemovesStaleEndpoints(t *testing.T) {
	initialEntries := []FCRegistryEntry{
		{
			Host:   "oblivion.petersimmons.com",
			Models: []FCModelSpec{{Name: "qwen3-32b-vllm", Port: 8000}},
		},
		{
			Host:   "precision.petersimmons.com",
			Models: []FCModelSpec{{Name: "bge-m3-infinity", Port: 8005}},
		},
	}

	// After first poll, precision goes offline
	reducedEntries := []FCRegistryEntry{
		{
			Host:   "oblivion.petersimmons.com",
			Models: []FCModelSpec{{Name: "qwen3-32b-vllm", Port: 8000}},
		},
	}

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(initialEntries)
		} else {
			json.NewEncoder(w).Encode(reducedEntries)
		}
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	poller := discovery.NewFCDiscoveryPoller(repo, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First poll: 2 endpoints
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("first Poll() error: %v", err)
	}
	all, _ := repo.GetAll(ctx)
	if len(all) != 2 {
		t.Errorf("after first poll: expected 2 endpoints, got %d", len(all))
	}

	// Second poll: precision removed from registry → should be removed from Olla
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("second Poll() error: %v", err)
	}
	all, _ = repo.GetAll(ctx)
	if len(all) != 1 {
		t.Errorf("after second poll: expected 1 endpoint (stale precision removed), got %d", len(all))
		for _, e := range all {
			t.Logf("  remaining: name=%q url=%q", e.Name, e.URLString)
		}
	}

	// Verify the remaining endpoint is oblivion only
	if len(all) == 1 && all[0].URLString != "http://oblivion.petersimmons.com:8000" {
		t.Errorf("expected oblivion endpoint, got %q", all[0].URLString)
	}
}

// TestFCEndpointRepository_PollFailOpenOnFCUnavailable verifies that when FC is
// unreachable, the existing endpoint set is preserved (fail-open for availability).
func TestFCEndpointRepository_PollFailOpenOnFCUnavailable(t *testing.T) {
	// Seed the repo with one endpoint
	repo := discovery.NewStaticEndpointRepository()
	priority := 100
	seedConfigs := []config.EndpointConfig{
		{URL: "http://oblivion.petersimmons.com:8000", Name: "oblivion", Type: "openai", Priority: &priority},
	}
	ctx := context.Background()
	if err := repo.LoadFromConfig(ctx, seedConfigs); err != nil {
		t.Fatalf("seed LoadFromConfig error: %v", err)
	}

	// Point at a server that is already closed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	poller := discovery.NewFCDiscoveryPoller(repo, srv.URL, newTestLogger())

	// Poll to an unavailable FC — should not error fatally, should preserve existing endpoints
	err := poller.Poll(ctx)
	if err == nil {
		t.Log("Poll returned nil (considered non-fatal by poller — acceptable)")
	}

	all, _ := repo.GetAll(ctx)
	if len(all) != 1 {
		t.Errorf("fail-open: expected original 1 endpoint preserved, got %d", len(all))
	}
}

func TestFCEndpointRepository_PollPreservesFleetEndpointMetadata(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "precision.petersimmons.com",
			Models: []FCModelSpec{
				{
					Name:                    "qwen3-coder-30b",
					Framework:               "vllm",
					EndpointType:            "vllm",
					Port:                    8000,
					Priority:                75,
					HealthCheckURL:          "/health",
					ModelURL:                "/v1/models",
					CircuitBreakerTimeout:   "2000s",
					CircuitBreakerThreshold: 10,
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	poller := discovery.NewFCDiscoveryPoller(repo, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v", err)
	}

	all, err := repo.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll() returned error: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(all))
	}

	got := all[0]
	if got.Type != "vllm" {
		t.Fatalf("Type = %q, want vllm", got.Type)
	}
	if got.Priority != 75 {
		t.Fatalf("Priority = %d, want 75", got.Priority)
	}
	if got.HealthCheckPathString != "/health" {
		t.Fatalf("HealthCheckPathString = %q, want /health", got.HealthCheckPathString)
	}
	if got.ModelURLString != "http://precision.petersimmons.com:8000/v1/models" {
		t.Fatalf("ModelURLString = %q, want v1 models URL", got.ModelURLString)
	}
	if got.CircuitBreakerTimeout != 2000*time.Second {
		t.Fatalf("CircuitBreakerTimeout = %v, want 2000s", got.CircuitBreakerTimeout)
	}
	if got.CircuitBreakerThreshold != 10 {
		t.Fatalf("CircuitBreakerThreshold = %d, want 10", got.CircuitBreakerThreshold)
	}
}

// TestFCEndpointRepository_PollPreRegistersModelsInRegistry verifies that after a
// successful poll, FC model names are pre-registered in the model registry so that
// routing decisions can be made before model discovery completes.
// This is the fix for petersimmons1972/olla#306 — the cold-start window where
// compatible_endpoints=0 for BAAI/bge-m3 until the 5-minute discovery cycle runs.
func TestFCEndpointRepository_PollPreRegistersModelsInRegistry(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "oblivion.petersimmons.com",
			Models: []FCModelSpec{
				{Name: "BAAI/bge-m3", Framework: "infinity", Port: 8003},
				{Name: "qwen3-coder-30b", Framework: "vllm", Port: 8000},
			},
		},
		{
			Host: "precision.petersimmons.com",
			Models: []FCModelSpec{
				{Name: "BAAI/bge-m3", Framework: "infinity", Port: 8005},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	modelReg := registry.NewMemoryModelRegistry(newTestLogger())
	poller := discovery.NewFCDiscoveryPollerWithRegistry(repo, modelReg, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v", err)
	}

	// BAAI/bge-m3 should be findable in the registry on both endpoints
	endpoints, err := modelReg.GetEndpointsForModel(ctx, "BAAI/bge-m3")
	if err != nil {
		t.Fatalf("GetEndpointsForModel() error: %v", err)
	}
	if len(endpoints) != 2 {
		t.Errorf("expected 2 endpoints for BAAI/bge-m3, got %d: %v", len(endpoints), endpoints)
	}

	// qwen3-coder-30b should be findable on oblivion only
	qwenEndpoints, err := modelReg.GetEndpointsForModel(ctx, "qwen3-coder-30b")
	if err != nil {
		t.Fatalf("GetEndpointsForModel(qwen3) error: %v", err)
	}
	if len(qwenEndpoints) != 1 {
		t.Errorf("expected 1 endpoint for qwen3-coder-30b, got %d: %v", len(qwenEndpoints), qwenEndpoints)
	}
	wantURL := "http://oblivion.petersimmons.com:8000"
	if len(qwenEndpoints) == 1 && qwenEndpoints[0] != wantURL {
		t.Errorf("expected qwen3 endpoint %q, got %q", wantURL, qwenEndpoints[0])
	}
}

// TestFCEndpointRepository_PollClearsStaleModelsOnRepoll verifies that when FC registry
// shrinks (model removed or host goes offline), the stale model→endpoint mapping is
// evicted from the model registry during the next poll.
func TestFCEndpointRepository_PollClearsStaleModelsOnRepoll(t *testing.T) {
	initialEntries := []FCRegistryEntry{
		{
			Host: "oblivion.petersimmons.com",
			Models: []FCModelSpec{
				{Name: "BAAI/bge-m3", Framework: "infinity", Port: 8003},
				{Name: "qwen3-coder-30b", Framework: "vllm", Port: 8000},
			},
		},
	}
	// Second poll: qwen3-coder-30b removed from oblivion
	reducedEntries := []FCRegistryEntry{
		{
			Host: "oblivion.petersimmons.com",
			Models: []FCModelSpec{
				{Name: "BAAI/bge-m3", Framework: "infinity", Port: 8003},
			},
		},
	}

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if callCount == 1 {
			json.NewEncoder(w).Encode(initialEntries)
		} else {
			json.NewEncoder(w).Encode(reducedEntries)
		}
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	modelReg := registry.NewMemoryModelRegistry(newTestLogger())
	poller := discovery.NewFCDiscoveryPollerWithRegistry(repo, modelReg, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First poll: both models registered
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("first Poll() error: %v", err)
	}
	endpoints, _ := modelReg.GetEndpointsForModel(ctx, "qwen3-coder-30b")
	if len(endpoints) != 1 {
		t.Errorf("after first poll: expected 1 endpoint for qwen3, got %d", len(endpoints))
	}

	// Second poll: qwen3 removed → model registry must evict stale mapping
	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("second Poll() error: %v", err)
	}
	endpoints, _ = modelReg.GetEndpointsForModel(ctx, "qwen3-coder-30b")
	if len(endpoints) != 0 {
		t.Errorf("after second poll: expected 0 endpoints for stale qwen3, got %d: %v", len(endpoints), endpoints)
	}
	// BAAI/bge-m3 should still be registered
	bgeEndpoints, _ := modelReg.GetEndpointsForModel(ctx, "BAAI/bge-m3")
	if len(bgeEndpoints) != 1 {
		t.Errorf("after second poll: expected 1 endpoint for BAAI/bge-m3, got %d", len(bgeEndpoints))
	}
}

func TestFCEndpointRepository_PollPreservesCapabilities(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "precision.petersimmons.com",
			Models: []FCModelSpec{
				{
					Name:         "nomic-embed-text",
					Capabilities: []string{"embeddings"},
					Port:         11434,
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	poller := discovery.NewFCDiscoveryPoller(repo, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v", err)
	}

	all, err := repo.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll() returned error: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(all))
	}
	if got := all[0].Capabilities; len(got) != 1 || got[0] != "embeddings" {
		t.Fatalf("Capabilities = %#v, want [\"embeddings\"]", got)
	}
}

func TestFCEndpointRepository_PollNoCapabilitiesIsNil(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "precision.petersimmons.com",
			Models: []FCModelSpec{
				{
					Name: "llama-3-70b",
					Port: 11434,
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	poller := discovery.NewFCDiscoveryPoller(repo, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v", err)
	}

	all, err := repo.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll() returned error: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(all))
	}
	if all[0].Capabilities != nil {
		t.Fatalf("Capabilities = %#v, want nil", all[0].Capabilities)
	}
}

// TestFCEndpointRepository_PollRegistersMultipleModelsSameEndpoint verifies that
// when FC reports two models on the SAME host:port, BOTH are registered in the
// model registry after sync. Because MemoryModelRegistry.RegisterModels replaces
// the full model list for a URL, models on the same endpoint must be grouped into
// a single RegisterModels call — otherwise the second clobbers the first
// (petersimmons1972/olla#4).
func TestFCEndpointRepository_PollRegistersMultipleModelsSameEndpoint(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "oblivion.petersimmons.com",
			Models: []FCModelSpec{
				{Name: "qwen3-coder-30b", Framework: "vllm", Port: 8000},
				{Name: "BAAI/bge-m3", Framework: "infinity", Port: 8000},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	modelReg := registry.NewMemoryModelRegistry(newTestLogger())
	poller := discovery.NewFCDiscoveryPollerWithRegistry(repo, modelReg, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v", err)
	}

	wantURL := "http://oblivion.petersimmons.com:8000"

	// Both models must be retrievable on the shared endpoint (no clobber).
	qwen, err := modelReg.GetEndpointsForModel(ctx, "qwen3-coder-30b")
	if err != nil {
		t.Fatalf("GetEndpointsForModel(qwen3) error: %v", err)
	}
	if len(qwen) != 1 || qwen[0] != wantURL {
		t.Errorf("qwen3-coder-30b: expected [%q], got %v", wantURL, qwen)
	}

	bge, err := modelReg.GetEndpointsForModel(ctx, "BAAI/bge-m3")
	if err != nil {
		t.Fatalf("GetEndpointsForModel(bge) error: %v", err)
	}
	if len(bge) != 1 || bge[0] != wantURL {
		t.Errorf("BAAI/bge-m3: expected [%q], got %v (second model clobbered first?)", wantURL, bge)
	}

	// The endpoint→model map must list BOTH models for the shared URL.
	emap, err := modelReg.GetEndpointModelMap(ctx)
	if err != nil {
		t.Fatalf("GetEndpointModelMap() error: %v", err)
	}
	em, ok := emap[wantURL]
	if !ok {
		t.Fatalf("expected URL %q present in endpoint model map", wantURL)
	}
	if len(em.Models) != 2 {
		names := make([]string, 0, len(em.Models))
		for _, m := range em.Models {
			names = append(names, m.Name)
		}
		t.Errorf("expected 2 models on %q, got %d: %v", wantURL, len(em.Models), names)
	}
}

// TestFCEndpointRepository_VLLMAndOpenAICompatibleTypesAccepted verifies that endpoints
// with type "vllm" or "openai-compatible" are accepted by FC discovery when the profile
// factory is initialised from built-in profiles only (no YAML on disk). This is the
// regression test for petersimmons1972/olla#57 where the built-in profile factory lacked
// vllm, causing a hard crash on the first FC poll in any environment where config/profiles/
// is unavailable (container without profiles dir, or NewFactoryWithDefaults failure fallback).
func TestFCEndpointRepository_VLLMAndOpenAICompatibleTypesAccepted(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "oblivion.petersimmons.com",
			Models: []FCModelSpec{
				{
					Name:         "qwen3-coder-30b",
					EndpointType: "vllm",
					Port:         8000,
				},
			},
		},
		{
			Host: "precision.petersimmons.com",
			Models: []FCModelSpec{
				{
					Name:         "bge-m3",
					EndpointType: "openai-compatible",
					Port:         8005,
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	// Use built-in-only factory (no YAML directory) to simulate the failure mode from #57:
	// NewFactoryWithDefaults() fails to find config/profiles/, falls back to NewFactory(""),
	// which only has the hardcoded built-in profiles. vllm must be recognised without YAML.
	builtinFactory, err := profile.NewFactory("")
	if err != nil {
		t.Fatalf("NewFactory(\"\") error: %v", err)
	}
	repo := discovery.NewStaticEndpointRepositoryWithFactory(builtinFactory)
	poller := discovery.NewFCDiscoveryPoller(repo, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v (vllm/openai-compatible types should be accepted by built-in profiles)", err)
	}

	all, err := repo.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll() returned error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 endpoints (vllm + openai-compatible), got %d", len(all))
	}

	typesSeen := make(map[string]bool)
	for _, ep := range all {
		typesSeen[ep.Type] = true
	}
	if !typesSeen["vllm"] {
		t.Errorf("expected endpoint with type \"vllm\" but not found; types seen: %v", typesSeen)
	}
	if !typesSeen["openai-compatible"] {
		t.Errorf("expected endpoint with type \"openai-compatible\" but not found; types seen: %v", typesSeen)
	}
}

// TestFCEndpointRepository_PollSyncsModelRegistryWithCapabilitiesAndRoutingNames
// verifies that modelName/loadedModel are used as the registry routing key (not the
// FC service name), and that capabilities are propagated into the model registry.
// This is the regression test for petersimmons1972/olla#60.
func TestFCEndpointRepository_PollSyncsModelRegistryWithCapabilitiesAndRoutingNames(t *testing.T) {
	entries := []FCRegistryEntry{
		{
			Host: "oblivion.petersimmons.com",
			Models: []FCModelSpec{
				{
					Name:         "embed-mi50",
					Port:         8003,
					ModelName:    "BAAI/bge-m3",
					LoadedModel:  "BAAI/bge-m3",
					Capabilities: []string{"embeddings"},
				},
				{
					Name:         "chat-mi50",
					Port:         8004,
					LoadedModel:  "meta-llama/Llama-3.1-8B-Instruct",
					Capabilities: []string{"chat"},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/registry" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	repo := discovery.NewStaticEndpointRepository()
	modelRegistry := registry.NewMemoryModelRegistry(newTestLogger())
	poller := discovery.NewFCDiscoveryPollerWithRegistry(repo, modelRegistry, srv.URL, newTestLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := poller.Poll(ctx); err != nil {
		t.Fatalf("Poll() returned error: %v", err)
	}

	// embed-mi50 should be reachable by its modelName, not its service name
	embedEndpoints, err := modelRegistry.GetEndpointsForModel(ctx, "BAAI/bge-m3")
	if err != nil {
		t.Fatalf("GetEndpointsForModel(embed) error: %v", err)
	}
	if len(embedEndpoints) != 1 || embedEndpoints[0] != "http://oblivion.petersimmons.com:8003" {
		t.Fatalf("expected embed model endpoint mapping, got %+v", embedEndpoints)
	}

	// chat-mi50 should be reachable by its loadedModel, not its service name
	chatEndpoints, err := modelRegistry.GetEndpointsForModel(ctx, "meta-llama/Llama-3.1-8B-Instruct")
	if err != nil {
		t.Fatalf("GetEndpointsForModel(chat) error: %v", err)
	}
	if len(chatEndpoints) != 1 || chatEndpoints[0] != "http://oblivion.petersimmons.com:8004" {
		t.Fatalf("expected loadedModel endpoint mapping, got %+v", chatEndpoints)
	}

	// capabilities must be preserved on the embed model
	models, err := modelRegistry.GetModelsForEndpoint(ctx, "http://oblivion.petersimmons.com:8003")
	if err != nil {
		t.Fatalf("GetModelsForEndpoint error: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model on embed endpoint, got %d", len(models))
	}
	if len(models[0].Capabilities) != 1 || models[0].Capabilities[0] != "embeddings" {
		t.Fatalf("expected embeddings capability, got %+v", models[0].Capabilities)
	}
}
