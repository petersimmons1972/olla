package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/thushan/olla/internal/config"
	"github.com/thushan/olla/internal/core/domain"
	"github.com/thushan/olla/internal/core/ports"
)

func TestBuildSystemSummary_ZeroEndpointsIsCritical(t *testing.T) {
	app := &Application{
		StartTime: time.Now().Add(-time.Minute),
	}

	summary := app.buildSystemSummary(
		nil,
		nil,
		ports.ProxyStats{
			TotalRequests:      10,
			SuccessfulRequests: 10,
		},
		ports.SecurityStats{},
		map[string]int64{},
		map[string]ports.EndpointStats{},
	)

	if summary.Status != statusCritical {
		t.Fatalf("expected zero-endpoint system summary to be %q, got %q", statusCritical, summary.Status)
	}
}

func TestBuildSystemSummary_HealthyIdleSystemIsNotCritical(t *testing.T) {
	app := &Application{
		StartTime: time.Now().Add(-time.Minute),
	}
	endpoint := &domain.Endpoint{
		Name:   "local",
		Status: domain.StatusHealthy,
	}

	summary := app.buildSystemSummary(
		[]*domain.Endpoint{endpoint},
		[]*domain.Endpoint{endpoint},
		ports.ProxyStats{},
		ports.SecurityStats{},
		map[string]int64{},
		map[string]ports.EndpointStats{},
	)

	if summary.Status != statusHealthy {
		t.Fatalf("expected healthy idle system summary to be %q, got %q", statusHealthy, summary.Status)
	}
}

func TestStatusHandlers_ConcurrentScrapesDoNotRace(t *testing.T) {
	modelType := "llm"
	family := "llama"
	quant := "q4"
	contextLength := int64(131072)
	now := time.Now()
	endpoint := &domain.Endpoint{
		Name:                "local",
		Type:                "ollama",
		URLString:           "http://localhost:11434",
		Status:              domain.StatusHealthy,
		Priority:            1,
		ConsecutiveFailures: 4,
		LastChecked:         now,
		NextCheckTime:       now.Add(time.Minute),
	}
	app := createTestStatusApplication([]*domain.Endpoint{endpoint})
	app.Config = &config.Config{Proxy: config.ProxyConfig{}}
	app.statsCollector = &mockStatusStatsCollector{
		endpointStats: map[string]ports.EndpointStats{
			endpoint.URLString: {
				TotalRequests:      20,
				SuccessfulRequests: 12,
				AverageLatency:     6000,
			},
		},
	}
	app.modelRegistry = &mockStatusModelRegistry{
		endpointModels: map[string]*domain.EndpointModels{
			endpoint.URLString: {
				EndpointURL: endpoint.URLString,
				LastUpdated: now,
				Models: []*domain.ModelInfo{
					{
						Name:     "llama3",
						Type:     "llm",
						LastSeen: now,
						Details: &domain.ModelDetails{
							Type:              &modelType,
							Family:            &family,
							QuantizationLevel: &quant,
							MaxContextLength:  &contextLength,
						},
					},
				},
			},
		},
	}

	const requests = 64
	start := make(chan struct{})
	errs := make(chan error, requests)
	var wg sync.WaitGroup

	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start

			var req *http.Request
			rec := httptest.NewRecorder()
			if i%2 == 0 {
				req = httptest.NewRequest(http.MethodGet, "/internal/status", nil)
				app.statusHandler(rec, req)
			} else {
				req = httptest.NewRequest(http.MethodGet, "/internal/status/models?detailed=true&group=family", nil)
				app.modelsStatusHandler(rec, req)
			}
			if rec.Code != http.StatusOK {
				errs <- fmt.Errorf("request %d got status %d: %s", i, rec.Code, rec.Body.String())
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
}
