package handlers

import (
	"testing"
	"time"

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
