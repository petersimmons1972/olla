package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/thushan/olla/internal/core/constants"
	"github.com/thushan/olla/internal/core/domain"
)

var healthResponseJSON = []byte(`{"status":"healthy"}`)

// healthHandler handles health check requests
func (a *Application) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(healthResponseJSON)
}

type readinessResponse struct {
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	RoutableEndpoints int    `json:"routable_endpoints"`
	RoutableModels    int    `json:"routable_models"`
}

func (a *Application) readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)

	resp := a.readinessStatus(r.Context())
	if resp.Status != "ready" {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *Application) readinessStatus(ctx context.Context) readinessResponse {
	if a.repository == nil {
		return readinessResponse{Status: "not_ready", Reason: "endpoint repository unavailable"}
	}

	routable, err := a.repository.GetRoutable(ctx)
	if err != nil {
		return readinessResponse{Status: "not_ready", Reason: "endpoint repository unavailable"}
	}
	if len(routable) == 0 {
		return readinessResponse{Status: "not_ready", Reason: "no routable endpoints"}
	}

	routableModels, err := a.countRoutableModels(ctx, routable)
	if err != nil {
		return readinessResponse{Status: "not_ready", Reason: "model registry unavailable", RoutableEndpoints: len(routable)}
	}
	if routableModels == 0 {
		return readinessResponse{Status: "not_ready", Reason: "no routable models", RoutableEndpoints: len(routable)}
	}

	return readinessResponse{Status: "ready", RoutableEndpoints: len(routable), RoutableModels: routableModels}
}

func (a *Application) countRoutableModels(ctx context.Context, routable []*domain.Endpoint) (int, error) {
	if a.modelRegistry == nil {
		return 0, nil
	}

	endpointModels, err := a.modelRegistry.GetEndpointModelMap(ctx)
	if err != nil {
		return 0, err
	}

	routableURLs := make(map[string]struct{}, len(routable))
	for _, endpoint := range routable {
		routableURLs[endpoint.GetURLString()] = struct{}{}
	}

	count := 0
	for endpointURL, models := range endpointModels {
		if _, ok := routableURLs[endpointURL]; !ok || models == nil {
			continue
		}
		count += len(models.Models)
	}
	return count, nil
}
