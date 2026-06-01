package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/thushan/olla/internal/core/domain"
)

func TestHealthHandlerIsProcessLiveness(t *testing.T) {
	app := &Application{}

	req := httptest.NewRequest(http.MethodGet, "/internal/health", nil)
	rec := httptest.NewRecorder()

	app.healthHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("healthHandler status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != `{"status":"healthy"}` {
		t.Fatalf("healthHandler body = %q, want static healthy response", rec.Body.String())
	}
}

func TestReadinessHandlerFailsWithoutRoutableModels(t *testing.T) {
	endpoints := []*domain.Endpoint{
		{
			Name:      "vllm",
			URLString: "http://vllm.local:8000",
			Status:    domain.StatusHealthy,
		},
	}
	app := &Application{
		repository:    &mockStatusEndpointRepository{endpoints: endpoints},
		modelRegistry: &mockStatusModelRegistry{},
	}

	req := httptest.NewRequest(http.MethodGet, "/internal/ready", nil)
	rec := httptest.NewRecorder()

	app.readinessHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readinessHandler status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestReadinessHandlerPassesWithRoutableModels(t *testing.T) {
	const endpointURL = "http://vllm.local:8000"
	endpoints := []*domain.Endpoint{
		{
			Name:      "vllm",
			URLString: endpointURL,
			Status:    domain.StatusHealthy,
		},
	}
	app := &Application{
		repository: &mockStatusEndpointRepository{endpoints: endpoints},
		modelRegistry: &mockStatusModelRegistry{
			endpointModels: map[string]*domain.EndpointModels{
				endpointURL: {
					LastUpdated: time.Now(),
					EndpointURL: endpointURL,
					Models:      []*domain.ModelInfo{{Name: "qwen3-coder"}},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/internal/ready", nil)
	rec := httptest.NewRecorder()

	app.readinessHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("readinessHandler status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
