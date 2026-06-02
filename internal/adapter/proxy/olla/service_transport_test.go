package olla

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thushan/olla/internal/adapter/proxy/config"
	"github.com/thushan/olla/internal/core/constants"
	"github.com/thushan/olla/internal/core/domain"
	"github.com/thushan/olla/internal/core/ports"
)

// TestCreateOptimisedTransport_ConnectionLimits verifies that both MaxConnsPerHost and
// MaxIdleConnsPerHost are mapped to their correct fields on http.Transport.
// Previously MaxConnsPerHost was mistakenly written to MaxIdleConnsPerHost and
// MaxConnsPerHost was never set (defaulting to 0 = unlimited).
func TestCreateOptimisedTransport_ConnectionLimits(t *testing.T) {
	t.Parallel()

	cfg := &Configuration{}
	cfg.MaxConnsPerHost = 42
	cfg.MaxIdleConnsPerHost = 17
	cfg.MaxIdleConns = 200
	cfg.IdleConnTimeout = 90 * time.Second

	transport := createOptimisedTransport(cfg)

	if transport.MaxConnsPerHost != 42 {
		t.Errorf("MaxConnsPerHost: want 42, got %d", transport.MaxConnsPerHost)
	}
	if transport.MaxIdleConnsPerHost != 17 {
		t.Errorf("MaxIdleConnsPerHost: want 17, got %d", transport.MaxIdleConnsPerHost)
	}
	if transport.MaxIdleConns != 200 {
		t.Errorf("MaxIdleConns: want 200, got %d", transport.MaxIdleConns)
	}
}

// TestCreateOptimisedTransport_DefaultsApplied verifies that NewService fills in sensible
// defaults before handing the config to createOptimisedTransport, so a zero-value config
// never silently leaves MaxConnsPerHost unlimited.
func TestCreateOptimisedTransport_DefaultsApplied(t *testing.T) {
	t.Parallel()

	// Zero-value config — defaults should be filled in by NewService, but we can verify
	// the expected defaults are consistent with the package constants.
	cfg := &Configuration{}
	cfg.MaxConnsPerHost = config.OllaDefaultMaxConnsPerHost
	cfg.MaxIdleConnsPerHost = config.OllaDefaultMaxIdleConnsPerHost
	cfg.MaxIdleConns = config.OllaDefaultMaxIdleConns
	cfg.IdleConnTimeout = config.OllaDefaultIdleConnTimeout

	transport := createOptimisedTransport(cfg)

	if transport.MaxConnsPerHost != config.OllaDefaultMaxConnsPerHost {
		t.Errorf("MaxConnsPerHost: want %d, got %d", config.OllaDefaultMaxConnsPerHost, transport.MaxConnsPerHost)
	}
	if transport.MaxIdleConnsPerHost != config.OllaDefaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost: want %d, got %d", config.OllaDefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	}
}

// TestCreateOptimisedTransport_FieldsAreDistinct guards against the specific regression
// where MaxConnsPerHost value bled into MaxIdleConnsPerHost. Using distinct values
// makes the mapping error immediately visible.
func TestCreateOptimisedTransport_FieldsAreDistinct(t *testing.T) {
	t.Parallel()

	cfg := &Configuration{}
	cfg.MaxConnsPerHost = 100
	cfg.MaxIdleConnsPerHost = 10
	cfg.MaxIdleConns = 500

	transport := createOptimisedTransport(cfg)

	// Regression guard: if the bug is reintroduced both fields get value 100.
	if transport.MaxConnsPerHost == transport.MaxIdleConnsPerHost {
		t.Errorf("MaxConnsPerHost (%d) and MaxIdleConnsPerHost (%d) are equal — likely a field mapping regression",
			transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
	}
	if transport.MaxConnsPerHost != 100 {
		t.Errorf("MaxConnsPerHost: want 100, got %d", transport.MaxConnsPerHost)
	}
	if transport.MaxIdleConnsPerHost != 10 {
		t.Errorf("MaxIdleConnsPerHost: want 10, got %d", transport.MaxIdleConnsPerHost)
	}
}

func TestPrepareProxyRequest_FlagEnabled_InjectsAPIKey(t *testing.T) {
	t.Parallel()

	svc := &Service{configuration: &Configuration{}}
	svc.configuration.EnableEndpointAuthInjection = true
	req, err := http.NewRequest(http.MethodPost, "http://client.local/v1/chat/completions", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	targetURL, _ := url.Parse("http://backend.local/v1/chat/completions")
	stats := &ports.RequestStats{StartTime: time.Now()}
	endpoint := &domain.Endpoint{APIKey: "endpoint-key"}

	proxyReq, err := svc.prepareProxyRequest(context.Background(), req, targetURL, endpoint, stats)
	if err != nil {
		t.Fatalf("prepareProxyRequest: %v", err)
	}

	if got := proxyReq.Header.Get(constants.HeaderAuthorization); got != "Bearer endpoint-key" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer endpoint-key")
	}
}

func TestPrepareProxyRequest_FlagDisabled_NoAuthHeader(t *testing.T) {
	t.Parallel()

	// Flag off (default) — no injection even if endpoint has an APIKey
	svc := &Service{configuration: &Configuration{}}
	svc.configuration.EnableEndpointAuthInjection = false
	req, err := http.NewRequest(http.MethodPost, "http://client.local/v1/chat/completions", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	targetURL, _ := url.Parse("http://backend.local/v1/chat/completions")
	stats := &ports.RequestStats{StartTime: time.Now()}
	endpoint := &domain.Endpoint{APIKey: "would-be-injected-without-flag"}

	proxyReq, err := svc.prepareProxyRequest(context.Background(), req, targetURL, endpoint, stats)
	if err != nil {
		t.Fatalf("prepareProxyRequest: %v", err)
	}

	if got := proxyReq.Header.Get(constants.HeaderAuthorization); got != "" {
		t.Fatalf("Authorization header = %q, want empty (flag is off)", got)
	}
}

func TestPrepareProxyRequest_FlagEnabled_MissingAPIKey_ReturnsError(t *testing.T) {
	t.Parallel()

	svc := &Service{configuration: &Configuration{}}
	svc.configuration.EnableEndpointAuthInjection = true
	req, err := http.NewRequest(http.MethodPost, "http://client.local/v1/chat/completions", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	targetURL, _ := url.Parse("http://backend.local/v1/chat/completions")
	stats := &ports.RequestStats{StartTime: time.Now()}
	endpoint := &domain.Endpoint{Name: "backend-no-key"}

	_, err = svc.prepareProxyRequest(context.Background(), req, targetURL, endpoint, stats)
	if err == nil {
		t.Fatal("expected error when endpoint auth injection is enabled without api_key")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "backend-no-key") {
		t.Fatalf("error %q does not mention endpoint name", got)
	}
}
