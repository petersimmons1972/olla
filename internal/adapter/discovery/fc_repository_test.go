package discovery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/thushan/olla/internal/adapter/discovery"
	"github.com/thushan/olla/internal/config"
	"github.com/thushan/olla/internal/logger"
)

// FCRegistryEntry mirrors the Flight Controller RegistryEntry type.
type FCRegistryEntry struct {
	Host   string        `json:"host"`
	Models []FCModelSpec `json:"models"`
}

type FCModelSpec struct {
	Name                    string `json:"name"`
	Framework               string `json:"framework,omitempty"`
	EndpointType            string `json:"endpointType,omitempty"`
	HealthCheckURL          string `json:"healthCheckURL,omitempty"`
	ModelURL                string `json:"modelURL,omitempty"`
	CircuitBreakerTimeout   string `json:"circuitBreakerTimeout,omitempty"`
	Port                    int    `json:"port"`
	Priority                int    `json:"priority,omitempty"`
	CircuitBreakerThreshold int    `json:"circuitBreakerThreshold,omitempty"`
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
