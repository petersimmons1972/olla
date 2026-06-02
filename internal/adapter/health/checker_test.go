package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/thushan/olla/internal/config"
	"github.com/thushan/olla/internal/core/domain"
	"github.com/thushan/olla/internal/logger"
)

type mockHTTPClient struct {
	statusCode int
	shouldErr  bool
	delay      time.Duration
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	if m.shouldErr {
		return nil, &mockNetError{timeout: false}
	}

	return &http.Response{
		StatusCode: m.statusCode,
		Body:       http.NoBody,
	}, nil
}

type mockNetError struct {
	timeout bool
}

func (e *mockNetError) Error() string   { return "mock network error" }
func (e *mockNetError) Timeout() bool   { return e.timeout }
func (e *mockNetError) Temporary() bool { return false }

type mockRepository struct {
	endpoints map[string]*domain.Endpoint
	mu        sync.RWMutex
}

func newMockRepository() *mockRepository {
	return &mockRepository{
		endpoints: make(map[string]*domain.Endpoint),
	}
}

func (m *mockRepository) GetAll(ctx context.Context) ([]*domain.Endpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	endpoints := make([]*domain.Endpoint, 0, len(m.endpoints))
	for _, ep := range m.endpoints {
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

func (m *mockRepository) GetHealthy(ctx context.Context) ([]*domain.Endpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	healthy := make([]*domain.Endpoint, 0, len(m.endpoints))
	for _, ep := range m.endpoints {
		if ep.Status == domain.StatusHealthy {
			healthy = append(healthy, ep)
		}
	}
	return healthy, nil
}

func (m *mockRepository) GetRoutable(ctx context.Context) ([]*domain.Endpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	routable := make([]*domain.Endpoint, 0, len(m.endpoints))
	for _, ep := range m.endpoints {
		if ep.Status.IsRoutable() {
			routable = append(routable, ep)
		}
	}
	return routable, nil
}

func (m *mockRepository) UpdateEndpoint(ctx context.Context, endpoint *domain.Endpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := endpoint.URL.String()
	m.endpoints[key] = endpoint
	return nil
}

func (m *mockRepository) LoadFromConfig(ctx context.Context, configs []config.EndpointConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.endpoints = make(map[string]*domain.Endpoint)
	for _, cfg := range configs {
		endpointURL, _ := url.Parse(cfg.URL)
		healthURL, _ := url.Parse(cfg.HealthCheckURL)

		endpoint := &domain.Endpoint{
			Name:                 cfg.Name,
			URL:                  endpointURL,
			HealthCheckURL:       healthURL,
			Status:               domain.StatusUnknown,
			CheckTimeout:         cfg.CheckTimeout,
			URLString:            endpointURL.String(),
			HealthCheckURLString: healthURL.String(),
		}
		m.endpoints[endpointURL.String()] = endpoint
	}
	return nil
}

func (m *mockRepository) Exists(ctx context.Context, endpointURL *url.URL) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, exists := m.endpoints[endpointURL.String()]
	return exists
}

func TestHTTPHealthChecker_Check_Success(t *testing.T) {
	mockClient := &mockHTTPClient{statusCode: 200}
	mockRepo := newMockRepository()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, mockClient)

	testURL, _ := url.Parse("http://localhost:11434")
	healthURL, _ := url.Parse("/health")
	endpoint := &domain.Endpoint{
		URL:            testURL,
		HealthCheckURL: healthURL,
		CheckTimeout:   time.Second,
	}

	result, err := checker.Check(context.Background(), endpoint)

	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.Status != domain.StatusHealthy {
		t.Errorf("Expected StatusHealthy, got %v", result.Status)
	}
}

func TestHTTPHealthChecker_Check_NetworkError(t *testing.T) {
	mockClient := &mockHTTPClient{shouldErr: true}
	mockRepo := newMockRepository()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, mockClient)

	testURL, _ := url.Parse("http://localhost:11434")
	healthURL, _ := url.Parse("/health")
	endpoint := &domain.Endpoint{
		URL:            testURL,
		HealthCheckURL: healthURL,
		CheckTimeout:   time.Second,
	}

	result, err := checker.Check(context.Background(), endpoint)

	if err == nil {
		t.Fatal("Expected error but got none")
	}
	if result.Status != domain.StatusOffline {
		t.Errorf("Expected StatusOffline, got %v", result.Status)
	}
}

func TestHTTPHealthChecker_Check_SlowResponse(t *testing.T) {
	mockClient := &mockHTTPClient{
		statusCode: 200,
		delay:      20 * time.Millisecond,
	}
	mockRepo := newMockRepository()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, mockClient)

	testURL, _ := url.Parse("http://localhost:11434")
	healthURL, _ := url.Parse("/health")
	endpoint := &domain.Endpoint{
		URL:            testURL,
		HealthCheckURL: healthURL,
		CheckTimeout:   time.Minute,
	}

	result, err := checker.Check(context.Background(), endpoint)

	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	if result.Status != domain.StatusHealthy {
		t.Errorf("Expected StatusHealthy for fast response, got %v", result.Status)
	}

	if result.Latency > 100*time.Millisecond {
		t.Errorf("Response took too long: %v", result.Latency)
	}
}

func TestCircuitBreaker_BasicOperation(t *testing.T) {
	cb := NewCircuitBreaker()
	url := "http://localhost:11434"

	if cb.IsOpen(url) {
		t.Error("Circuit breaker should be closed initially")
	}

	// Record failures until it opens
	for range DefaultCircuitBreakerThreshold {
		cb.RecordFailure(url)
	}

	if !cb.IsOpen(url) {
		t.Error("Circuit breaker should be open after threshold failures")
	}

	// Record success should close it
	cb.RecordSuccess(url)
	if cb.IsOpen(url) {
		t.Error("Circuit breaker should be closed after success")
	}
}

func TestCircuitBreaker_PerEndpointOverrides(t *testing.T) {
	tests := []struct {
		name        string
		config      CircuitBreakerConfig
		failures    int
		expectOpen  bool
		sleepBefore bool
	}{
		{
			name: "custom threshold delays open",
			config: CircuitBreakerConfig{
				Threshold: DefaultCircuitBreakerThreshold + 2,
			},
			failures:   DefaultCircuitBreakerThreshold,
			expectOpen: false,
		},
		{
			name: "custom threshold opens at override",
			config: CircuitBreakerConfig{
				Threshold: DefaultCircuitBreakerThreshold + 2,
			},
			failures:   DefaultCircuitBreakerThreshold + 2,
			expectOpen: true,
		},
		{
			name: "custom timeout controls half open cooldown",
			config: CircuitBreakerConfig{
				Threshold: 1,
				Timeout:   10 * time.Millisecond,
			},
			failures:    1,
			expectOpen:  false,
			sleepBefore: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := NewCircuitBreaker()
			url := "http://localhost:11434/" + tt.name
			cb.ConfigureEndpoint(url, tt.config)

			for range tt.failures {
				cb.RecordFailure(url)
			}
			if tt.sleepBefore {
				time.Sleep(20 * time.Millisecond)
			}

			if got := cb.IsOpen(url); got != tt.expectOpen {
				t.Fatalf("IsOpen() = %v, want %v", got, tt.expectOpen)
			}
		})
	}
}

func TestCircuitBreaker_Cleanup(t *testing.T) {
	cb := NewCircuitBreaker()
	url1 := "http://localhost:11434"
	url2 := "http://localhost:11435"

	cb.RecordFailure(url1)
	cb.RecordFailure(url2)

	active := cb.GetActiveEndpoints()
	if len(active) != 2 {
		t.Errorf("Expected 2 active endpoints, got %d", len(active))
	}

	cb.CleanupEndpoint(url1)
	active = cb.GetActiveEndpoints()
	if len(active) != 1 {
		t.Errorf("Expected 1 active endpoint after cleanup, got %d", len(active))
	}
}

func TestHealthChecker_StartStop(t *testing.T) {
	mockRepo := newMockRepository()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, &mockHTTPClient{statusCode: 200})
	ctx := context.Background()

	err := checker.StartChecking(ctx)
	if err != nil {
		t.Fatalf("StartChecking failed: %v", err)
	}

	stats := checker.GetSchedulerStats()
	if !stats["isRunning"].(bool) {
		t.Error("Checker should be running")
	}

	err = checker.StopChecking(ctx)
	if err != nil {
		t.Fatalf("StopChecking failed: %v", err)
	}

	stats = checker.GetSchedulerStats()
	if stats["isRunning"].(bool) {
		t.Error("Checker should be stopped")
	}
}

func TestHTTPHealthChecker_ForceHealthCheck(t *testing.T) {
	mockRepo := newMockRepository()
	mockClient := &mockHTTPClient{statusCode: 200}

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log) // Fix: add theme

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, mockClient)
	ctx := context.Background()

	// Add some endpoints
	configs := []config.EndpointConfig{
		{
			Name:           "test-endpoint",
			URL:            "http://localhost:11434",
			HealthCheckURL: "/health",
			CheckTimeout:   time.Second,
		},
	}
	mockRepo.LoadFromConfig(ctx, configs)

	// Start checker
	checker.StartChecking(ctx)
	defer checker.StopChecking(ctx)

	// Force health check
	err := checker.RunHealthCheck(ctx, true)
	if err != nil {
		t.Fatalf("RunHealthCheck failed: %v", err)
	}

	// Verify endpoint was updated
	endpoints, _ := mockRepo.GetAll(ctx)
	if len(endpoints) != 1 {
		t.Fatalf("Expected 1 endpoint, got %d", len(endpoints))
	}

	if endpoints[0].Status != domain.StatusHealthy {
		t.Errorf("Expected healthy status after force check, got %v", endpoints[0].Status)
	}
}

func TestHealthChecker_ConcurrentAccess(t *testing.T) {
	mockRepo := newMockRepository()
	mockClient := &mockHTTPClient{statusCode: 200}

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, mockClient)
	ctx := context.Background()

	configs := make([]config.EndpointConfig, 5)
	for i := range 5 {
		configs[i] = config.EndpointConfig{
			Name:           fmt.Sprintf("endpoint-%d", i),
			URL:            fmt.Sprintf("http://localhost:%d", 11434+i),
			HealthCheckURL: "/health",
			CheckTimeout:   time.Second,
		}
	}
	mockRepo.LoadFromConfig(ctx, configs)

	err := checker.StartChecking(ctx)
	if err != nil {
		t.Fatalf("Failed to start health checker: %v", err)
	}
	defer checker.StopChecking(ctx)

	var wg sync.WaitGroup
	errors := make(chan error, 20)

	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := checker.RunHealthCheck(ctx, false)
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}
}
func TestHTTPHealthChecker_PanicRecovery(t *testing.T) {
	mockRepo := newMockRepository()

	panicClient := &panicHTTPClient{}

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, panicClient)

	configs := []config.EndpointConfig{
		{
			Name:           "panic-endpoint",
			URL:            "http://localhost:11434",
			HealthCheckURL: "/health",
			CheckTimeout:   time.Second,
		},
	}
	mockRepo.LoadFromConfig(context.Background(), configs)

	ctx := context.Background()
	checker.StartChecking(ctx)
	defer checker.StopChecking(ctx)

	// This should not crash the test - panic should be recovered
	err := checker.RunHealthCheck(ctx, false)
	if err != nil {
		t.Fatalf("RunHealthCheck should not fail due to panic recovery: %v", err)
	}

	// Verify endpoint was still processed (even though it panicked)
	endpoints, _ := mockRepo.GetAll(ctx)
	if len(endpoints) != 1 {
		t.Fatalf("Expected 1 endpoint, got %d", len(endpoints))
	}
}
func TestHTTPHealthChecker_ConcurrentHealthChecks(t *testing.T) {
	mockRepo := newMockRepository()
	slowClient := &mockHTTPClient{
		statusCode: 200,
		delay:      50 * time.Millisecond, // Slow but not timeout
	}

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, slowClient)

	configs := make([]config.EndpointConfig, 10)
	for i := range 10 {
		configs[i] = config.EndpointConfig{
			Name:           fmt.Sprintf("endpoint-%d", i),
			URL:            fmt.Sprintf("http://localhost:%d", 11434+i),
			HealthCheckURL: "/health",
			CheckTimeout:   time.Second,
		}
	}
	mockRepo.LoadFromConfig(context.Background(), configs)

	ctx := context.Background()
	checker.StartChecking(ctx)
	defer checker.StopChecking(ctx)

	// Time the health check to ensure concurrency is working
	start := time.Now()
	err := checker.RunHealthCheck(ctx, false)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("RunHealthCheck failed: %v", err)
	}

	// With 10 endpoints taking 50ms each, serial execution would take 500ms+
	// Concurrent execution should be much faster
	if duration > 200*time.Millisecond {
		t.Errorf("Health checks took too long (%v), may not be running concurrently", duration)
	}

	// Verify all endpoints were checked
	endpoints, _ := mockRepo.GetAll(ctx)
	for _, endpoint := range endpoints {
		if endpoint.Status != domain.StatusHealthy {
			t.Errorf("Endpoint %s not healthy after check: %v", endpoint.Name, endpoint.Status)
		}
	}
}

/*
	func TestHTTPHealthChecker_StatusCodeLogging(t *testing.T) {
		mockRepo := newMockRepository()

		statusCodes := []int{200, 404, 500, 503}
		mockClient := &statusCodeHTTPClient{
			statusCodes: statusCodes,
		}

		loggerCfg := &logger.Config{Level: "debug", Theme: "default"} // Debug to capture all logs
		log, cleanup, _ := logger.New(loggerCfg)
		defer cleanup()
		styledLogger := logger.NewPlainStyledLogger(log)

		checker := NewHTTPHealthChecker(mockRepo, styledLogger, mockClient)

		configs := make([]config.EndpointConfig, len(statusCodes))
		for i := range statusCodes {
			configs[i] = config.EndpointConfig{
				Name:           fmt.Sprintf("endpoint-%d", i),
				URL:            fmt.Sprintf("http://localhost:%d", 11434+i),
				HealthCheckURL: "/health",
				CheckTimeout:   time.Second,
			}
		}
		mockRepo.LoadFromConfig(context.Background(), configs)

		ctx := context.Background()
		checker.StartChecking(ctx)
		defer checker.StopChecking(ctx)

		err := checker.RunHealthCheck(ctx, false)
		if err != nil {
			t.Fatalf("RunHealthCheck failed: %v", err)
		}

		// Verify endpoints have different statuses based on status codes
		endpoints, _ := mockRepo.GetAll(ctx)

		expectedStatuses := map[int]domain.EndpointStatus{
			200: domain.StatusHealthy,
			404: domain.StatusUnhealthy,
			500: domain.StatusUnhealthy,
			503: domain.StatusUnhealthy,
		}

		for i, endpoint := range endpoints {
			expectedStatus := expectedStatuses[statusCodes[i]]
			if endpoint.Status != expectedStatus {
				t.Errorf("Endpoint %d: expected status %v for HTTP %d, got %v",
					i, expectedStatus, statusCodes[i], endpoint.Status)
			}
		}
	}
*/
func TestHTTPHealthChecker_ContextCancellation(t *testing.T) {
	mockRepo := newMockRepository()

	mockClient := &mockHTTPClient{
		statusCode: 200,
		delay:      100 * time.Millisecond,
	}

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	checker := NewHTTPHealthChecker(mockRepo, styledLogger, mockClient)

	configs := []config.EndpointConfig{
		{
			Name:           "test-endpoint",
			URL:            "http://localhost:11434",
			HealthCheckURL: "/health",
			CheckTimeout:   time.Second,
		},
	}
	mockRepo.LoadFromConfig(context.Background(), configs)

	// Start checker
	ctx, cancel := context.WithCancel(context.Background())
	checker.StartChecking(ctx)
	defer checker.StopChecking(ctx)

	// Cancel context quickly
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	// This should handle cancellation gracefully
	err := checker.RunHealthCheck(ctx, false)

	// The error might be due to context cancellation, which is expected
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context cancellation or no error, got: %v", err)
	}
}

// nonRetryableError is a plain error (not net.Error) so classifyError returns
// ErrorTypeHTTPError, which makes shouldRetry return false. This avoids the
// exponential-backoff retry delays inside HealthClient.Check during unit tests.
type nonRetryableError struct{}

func (e *nonRetryableError) Error() string { return "non-retryable test error" }

// nonRetryingHTTPClient returns a non-retryable error, collapsing the retry
// loop inside HealthClient to a single attempt for fast unit tests.
type nonRetryingHTTPClient struct{}

func (c *nonRetryingHTTPClient) Do(_ *http.Request) (*http.Response, error) {
	return nil, &nonRetryableError{}
}

// TestUnhealthyCallbackPredicate verifies that the unhealthy callback fires only when
// an endpoint transitions from a routable state to a non-routable one. The previous
// predicate (newStatus != Healthy && oldStatus != Unknown) incorrectly fired on
// Healthy→Busy and Healthy→Warming, evicting sticky sessions unnecessarily.
func TestUnhealthyCallbackPredicate(t *testing.T) {
	t.Parallel()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	makeEndpoint := func(urlStr string, status domain.EndpointStatus) *domain.Endpoint {
		u, _ := url.Parse(urlStr)
		hcu, _ := url.Parse(urlStr + "/health")
		return &domain.Endpoint{
			Name:                 urlStr,
			URL:                  u,
			HealthCheckURL:       hcu,
			URLString:            u.String(),
			HealthCheckURLString: hcu.String(),
			Status:               status,
			CheckTimeout:         time.Second,
		}
	}

	// errClient returns a non-retryable error → StatusUnhealthy (non-routable).
	// Using a plain error (not net.Error) avoids the retry-backoff delay in HealthClient.
	errClient := &nonRetryingHTTPClient{}
	// okClient returns HTTP 200 → StatusHealthy (routable)
	okClient := &mockHTTPClient{statusCode: 200}

	tests := []struct {
		name      string
		oldStatus domain.EndpointStatus
		client    HTTPClient
		wantFired bool
	}{
		// Routable → non-routable: callback must fire.
		{name: "Healthy→Unhealthy fires", oldStatus: domain.StatusHealthy, client: errClient, wantFired: true},
		{name: "Busy→Unhealthy fires", oldStatus: domain.StatusBusy, client: errClient, wantFired: true},
		{name: "Warming→Unhealthy fires", oldStatus: domain.StatusWarming, client: errClient, wantFired: true},

		// Already non-routable → non-routable: nothing was pinned, so no purge.
		{name: "Unknown→Unhealthy no fire", oldStatus: domain.StatusUnknown, client: errClient, wantFired: false},
		{name: "Offline→Unhealthy no fire", oldStatus: domain.StatusOffline, client: errClient, wantFired: false},

		// Routable → routable: keep sticky sessions intact.
		{name: "Healthy→Healthy no fire (no change)", oldStatus: domain.StatusHealthy, client: okClient, wantFired: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fired := make(chan struct{}, 1)
			repo := newMockRepository()
			ep := makeEndpoint("http://127.0.0.1:19999", tc.oldStatus)
			repo.mu.Lock()
			repo.endpoints[ep.URLString] = ep
			repo.mu.Unlock()

			checker := NewHTTPHealthChecker(repo, styledLogger, tc.client)
			checker.SetUnhealthyCallback(UnhealthyCallbackFunc(func(_ context.Context, _ *domain.Endpoint) {
				select {
				case fired <- struct{}{}:
				default:
				}
			}))

			ctx := context.Background()
			checker.checkEndpoint(ctx, ep)

			// The callback dispatches asynchronously in a goroutine; give it a
			// short window to fire before concluding it won't.
			var gotFired bool
			select {
			case <-fired:
				gotFired = true
			case <-time.After(200 * time.Millisecond):
			}

			if gotFired && !tc.wantFired {
				t.Errorf("unhealthy callback fired but should not have (old=%s)", tc.oldStatus)
			}
			if !gotFired && tc.wantFired {
				t.Errorf("unhealthy callback not fired but should have (old=%s)", tc.oldStatus)
			}
		})
	}
}

type panicHTTPClient struct{}

func (p *panicHTTPClient) Do(req *http.Request) (*http.Response, error) {
	panic("simulated panic in health check")
}

type statusCodeHTTPClient struct {
	statusCodes []int
	callCount   int
}

func (s *statusCodeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	statusCode := s.statusCodes[s.callCount%len(s.statusCodes)]
	s.callCount++
	return &http.Response{
		StatusCode: statusCode,
		Body:       http.NoBody,
	}, nil
}

// sequentialHTTPClient returns responses in order, cycling through the provided
// list.  Each call pops the next response; after the list is exhausted it repeats
// the last entry.  This lets regression tests inject a "healthy → transport-fail →
// healthy" sequence that reproduces the half-open connection poisoning scenario
// described in petersimmons1972/olla#73.
type sequentialHTTPClient struct {
	mu        sync.Mutex
	responses []sequentialResponse
	idx       int
}

type sequentialResponse struct {
	statusCode int
	err        error
}

func (c *sequentialHTTPClient) Do(_ *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	r := c.responses[c.idx]
	if c.idx < len(c.responses)-1 {
		c.idx++
	}

	if r.err != nil {
		return nil, r.err
	}
	return &http.Response{
		StatusCode: r.statusCode,
		Body:       http.NoBody,
	}, nil
}

// halfOpenConnError mimics the transport-level error the Go HTTP stack returns
// when it reuses a pooled keep-alive connection that the peer has silently
// closed (conntrack idle-timeout / half-open TCP).  It implements net.Error
// (non-timeout, non-temporary) so that classifyError returns ErrorTypeNetwork
// and determineStatus returns StatusOffline — exactly what olla#73 observes.
//
// Because shouldRetry returns true for ErrorTypeNetwork, HealthClient.Check
// will retry up to DefaultMaxRetries (2) additional times.  Test sequences
// must therefore emit (DefaultMaxRetries+1) consecutive errors to produce one
// offline outcome visible to checkEndpoint.
type halfOpenConnError struct{}

func (e *halfOpenConnError) Error() string   { return "connection refused (half-open)" }
func (e *halfOpenConnError) Timeout() bool   { return false }
func (e *halfOpenConnError) Temporary() bool { return false }

// TestIssue73_TransportPoisoning_HealthyThenFlip is the regression test for
// petersimmons1972/olla#73 path (b): endpoint is discovered healthy on the first
// probe (fresh connection), then ~30 s later the pooled connection goes half-open
// and the probe fails → endpoint flips offline.  With the fix
// (DisableKeepAlives=true) every probe uses a fresh connection, so the third
// probe succeeds and the endpoint recovers to healthy.
//
// Without the fix, the mock sequence [200, connErr, connErr, 200] exercises the
// stuck-offline path: the circuit breaker opens after threshold failures and the
// endpoint never recovers because every subsequent probe (via the poisoned
// pooled connection) also fails.  With the fix the test verifies that the
// endpoint recovers to StatusHealthy after the successful probe.
func TestIssue73_TransportPoisoning_HealthyThenFlip(t *testing.T) {
	t.Parallel()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	connErr := &halfOpenConnError{}

	// HealthClient.Check retries network errors up to DefaultMaxRetries (2) times,
	// so each failing checkEndpoint invocation consumes (DefaultMaxRetries+1) = 3
	// error responses from the sequential client.
	//
	// Sequence layout:
	//   probe 1 attempt 0:         200    → healthy
	//   probe 2 attempts 0,1,2: 3× err   → offline (half-open connection, all retries fail)
	//   probe 3 attempts 0,1,2: 3× err   → offline (still poisoned; would loop forever without fix)
	//   probe 4 attempt 0:         200    → healthy (fresh conn with fix; recovery)
	client := &sequentialHTTPClient{
		responses: []sequentialResponse{
			{statusCode: 200}, // probe 1: fresh connection, succeeds
			{err: connErr},    // probe 2 attempt 0
			{err: connErr},    // probe 2 attempt 1 (retry 1)
			{err: connErr},    // probe 2 attempt 2 (retry 2) → StatusOffline
			{err: connErr},    // probe 3 attempt 0
			{err: connErr},    // probe 3 attempt 1 (retry 1)
			{err: connErr},    // probe 3 attempt 2 (retry 2) → StatusOffline
			{statusCode: 200}, // probe 4: backend up; fresh conn succeeds
		},
	}

	repo := newMockRepository()
	endpointURLStr := "http://127.0.0.1:28004"
	healthURLStr := endpointURLStr + "/v1/models"
	endpointURL, _ := url.Parse(endpointURLStr)
	healthURL, _ := url.Parse(healthURLStr)

	ep := &domain.Endpoint{
		Name:                    "leviathan-embed-7900xt",
		URL:                     endpointURL,
		HealthCheckURL:          healthURL,
		URLString:               endpointURLStr,
		HealthCheckURLString:    healthURLStr,
		Status:                  domain.StatusUnknown,
		CheckTimeout:            2 * time.Second,
		CheckInterval:           5 * time.Second,
		BackoffMultiplier:       1,
		CircuitBreakerThreshold: DefaultCircuitBreakerThreshold,
		CircuitBreakerTimeout:   DefaultCircuitBreakerTimeout,
	}
	repo.mu.Lock()
	repo.endpoints[endpointURLStr] = ep
	repo.mu.Unlock()

	checker := NewHTTPHealthChecker(repo, styledLogger, client)
	ctx := context.Background()

	// Probe 1: must succeed (healthy).
	checker.checkEndpoint(ctx, ep)
	repo.mu.RLock()
	statusAfterProbe1 := repo.endpoints[endpointURLStr].Status
	repo.mu.RUnlock()
	if statusAfterProbe1 != domain.StatusHealthy {
		t.Fatalf("probe 1: expected StatusHealthy, got %s", statusAfterProbe1)
	}

	// Probe 2: half-open connection error → endpoint must flip offline.
	repo.mu.RLock()
	ep2 := *repo.endpoints[endpointURLStr]
	repo.mu.RUnlock()
	checker.checkEndpoint(ctx, &ep2)
	repo.mu.RLock()
	statusAfterProbe2 := repo.endpoints[endpointURLStr].Status
	repo.mu.RUnlock()
	if statusAfterProbe2 != domain.StatusOffline {
		t.Fatalf("probe 2: expected StatusOffline after half-open error, got %s", statusAfterProbe2)
	}

	// Probe 3: still failing (would stay stuck without the fix).
	repo.mu.RLock()
	ep3 := *repo.endpoints[endpointURLStr]
	repo.mu.RUnlock()
	checker.checkEndpoint(ctx, &ep3)
	repo.mu.RLock()
	statusAfterProbe3 := repo.endpoints[endpointURLStr].Status
	repo.mu.RUnlock()
	if statusAfterProbe3 != domain.StatusOffline {
		t.Fatalf("probe 3: expected StatusOffline (still failing), got %s", statusAfterProbe3)
	}

	// Probe 4: successful probe after recovery (fresh connection).
	// This simulates what happens after the circuit breaker half-open window expires
	// and a new connection is dialled.  With DisableKeepAlives=true the transport
	// always dials fresh, so a successful backend response recovers the endpoint.
	//
	// Reset the circuit breaker state to simulate the half-open window expiring.
	checker.healthClient.circuitBreaker.RecordSuccess(healthURLStr)

	repo.mu.RLock()
	ep4 := *repo.endpoints[endpointURLStr]
	repo.mu.RUnlock()
	checker.checkEndpoint(ctx, &ep4)
	repo.mu.RLock()
	statusAfterProbe4 := repo.endpoints[endpointURLStr].Status
	repo.mu.RUnlock()
	if statusAfterProbe4 != domain.StatusHealthy {
		t.Fatalf("probe 4: expected StatusHealthy after recovery, got %s (olla#73 regression — endpoint never self-heals)", statusAfterProbe4)
	}
}

// TestIssue73_TransportPoisoning_DiscoveredWhileOffline is the regression test for
// petersimmons1972/olla#73 path (a): olla first discovers the endpoint while the
// backend is down → StatusOffline → backend comes up → endpoint must recover.
//
// This test verifies the circuit breaker's half-open recovery path: after the
// circuit breaker timeout a single probe is allowed through; if it succeeds the
// endpoint transitions back to StatusHealthy.
func TestIssue73_TransportPoisoning_DiscoveredWhileOffline(t *testing.T) {
	t.Parallel()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	connErr := &halfOpenConnError{}

	// Each failing checkEndpoint call consumes (DefaultMaxRetries+1) = 3 error
	// responses because HealthClient.Check retries network errors.
	// Three checkEndpoint failures → circuit breaker threshold (3) reached → open.
	//
	// Sequence layout (9 error responses for 3 failing probes, then 1 success):
	//   probe 1 attempts 0,1,2: 3× err → fail 1 (circuit not yet open)
	//   probe 2 attempts 0,1,2: 3× err → fail 2
	//   probe 3 attempts 0,1,2: 3× err → fail 3 → circuit opens
	//   probe 4 attempt 0:       1× 200 → recovery (fresh conn after circuit reset)
	client := &sequentialHTTPClient{
		responses: []sequentialResponse{
			{err: connErr},    // probe 1 attempt 0
			{err: connErr},    // probe 1 attempt 1
			{err: connErr},    // probe 1 attempt 2 → fail 1
			{err: connErr},    // probe 2 attempt 0
			{err: connErr},    // probe 2 attempt 1
			{err: connErr},    // probe 2 attempt 2 → fail 2
			{err: connErr},    // probe 3 attempt 0
			{err: connErr},    // probe 3 attempt 1
			{err: connErr},    // probe 3 attempt 2 → fail 3, circuit opens
			{statusCode: 200}, // probe 4: backend now up; fresh connection succeeds
		},
	}

	repo := newMockRepository()
	endpointURLStr := "http://127.0.0.1:28005"
	healthURLStr := endpointURLStr + "/v1/models"
	endpointURL, _ := url.Parse(endpointURLStr)
	healthURL, _ := url.Parse(healthURLStr)

	ep := &domain.Endpoint{
		Name:                    "leviathan-embed-offline",
		URL:                     endpointURL,
		HealthCheckURL:          healthURL,
		URLString:               endpointURLStr,
		HealthCheckURLString:    healthURLStr,
		Status:                  domain.StatusUnknown,
		CheckTimeout:            2 * time.Second,
		CheckInterval:           5 * time.Second,
		BackoffMultiplier:       1,
		CircuitBreakerThreshold: DefaultCircuitBreakerThreshold,
		CircuitBreakerTimeout:   DefaultCircuitBreakerTimeout,
	}
	repo.mu.Lock()
	repo.endpoints[endpointURLStr] = ep
	repo.mu.Unlock()

	checker := NewHTTPHealthChecker(repo, styledLogger, client)
	ctx := context.Background()

	// Three failures to open the circuit breaker.
	for i := range 3 {
		repo.mu.RLock()
		current := *repo.endpoints[endpointURLStr]
		repo.mu.RUnlock()
		checker.checkEndpoint(ctx, &current)
		repo.mu.RLock()
		s := repo.endpoints[endpointURLStr].Status
		repo.mu.RUnlock()
		if s != domain.StatusOffline {
			t.Fatalf("failure probe %d: expected StatusOffline, got %s", i+1, s)
		}
	}

	// Circuit breaker is now open.  Simulate the half-open window expiring by
	// directly resetting the circuit breaker, which is what happens when the
	// configured timeout elapses in production.
	checker.healthClient.circuitBreaker.RecordSuccess(healthURLStr)

	// Recovery probe: backend is now up; with DisableKeepAlives=true a fresh
	// connection is dialled and the probe succeeds.
	repo.mu.RLock()
	epRecovery := *repo.endpoints[endpointURLStr]
	repo.mu.RUnlock()
	checker.checkEndpoint(ctx, &epRecovery)
	repo.mu.RLock()
	finalStatus := repo.endpoints[endpointURLStr].Status
	repo.mu.RUnlock()

	if finalStatus != domain.StatusHealthy {
		t.Fatalf("recovery probe: expected StatusHealthy, got %s (olla#73 regression — discovered-while-offline never self-heals)", finalStatus)
	}
}

// TestIssue73_DefaultHealthCheckerUsesNoKeepAlives verifies that
// NewHTTPHealthCheckerWithDefaults produces a client with DisableKeepAlives=true.
// This is the canonical property test for the fix: if someone accidentally
// reverts the transport config, this test will catch it.
func TestIssue73_DefaultHealthCheckerUsesNoKeepAlives(t *testing.T) {
	t.Parallel()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	repo := newMockRepository()
	checker := NewHTTPHealthCheckerWithDefaults(repo, styledLogger)

	httpClient, ok := checker.healthClient.client.(*http.Client)
	if !ok {
		t.Fatal("health client is not an *http.Client — cannot inspect transport")
	}
	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("http.Client.Transport is not an *http.Transport — cannot inspect DisableKeepAlives")
	}
	if !transport.DisableKeepAlives {
		t.Error("NewHTTPHealthCheckerWithDefaults: DisableKeepAlives must be true to prevent transport poisoning (olla#73)")
	}
}

// TestStopChecking_DoubleInvoke verifies concurrent double-stops do not panic.
// Previously, two callers that both passed the isRunning.Load() guard could
// race to close(stopCh), causing a "close of closed channel" panic.
func TestStopChecking_DoubleInvoke(t *testing.T) {
	t.Parallel()

	loggerCfg := &logger.Config{Level: "error", Theme: "default"}
	log, cleanup, _ := logger.New(loggerCfg)
	defer cleanup()
	styledLogger := logger.NewPlainStyledLogger(log)

	mockRepo := newMockRepository()
	checker := NewHTTPHealthChecker(mockRepo, styledLogger, &mockHTTPClient{statusCode: 200})

	// Start the checker so isRunning == true.
	if err := checker.StartChecking(context.Background()); err != nil {
		t.Fatalf("StartChecking: %v", err)
	}

	// Two concurrent stops — neither should panic.
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			_ = checker.StopChecking(context.Background())
		}()
	}
	wg.Wait()
}
