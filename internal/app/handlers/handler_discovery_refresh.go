package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/thushan/olla/internal/config"
	"github.com/thushan/olla/internal/core/constants"
)

type DiscoveryRefreshResponse struct {
	Timestamp time.Time `json:"timestamp"`
	Refreshed bool      `json:"refreshed"`
}

func (a *Application) discoveryRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if a.discoveryService == nil {
		http.Error(w, "Discovery service not initialized", http.StatusServiceUnavailable)
		return
	}

	if err := a.discoveryService.RefreshEndpoints(r.Context()); err != nil {
		http.Error(w, "Discovery refresh failed", http.StatusInternalServerError)
		return
	}

	refreshed := true
	if a.Config != nil && a.Config.Discovery.Type == config.DefaultDiscoveryType {
		refreshed = false
	}

	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(DiscoveryRefreshResponse{
		Timestamp: time.Now(),
		Refreshed: refreshed,
	})
}
