package routing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/thushan/olla/internal/config"
	"github.com/thushan/olla/internal/core/constants"
	"github.com/thushan/olla/internal/core/domain"
)

func TestDiscoveryStrategy_CapabilityFilterEmbed(t *testing.T) {
	ctx := context.WithValue(context.Background(), constants.ContextModelCapabilitiesKey, &domain.ModelCapabilities{
		Embeddings: true,
	})

	healthyEndpoints := []*domain.Endpoint{
		{Name: "embed", URLString: "http://embedding", Capabilities: []string{"embeddings"}},
		{Name: "chat", URLString: "http://chat", Capabilities: []string{"chat"}},
	}
	modelEndpoints := []string{"http://embedding", "http://chat"}

	strategy := &DiscoveryStrategy{
		options: config.ModelRoutingStrategyOptions{
			DiscoveryRefreshOnMiss: true,
		},
		logger:         createTestLogger(),
		strictFallback: NewStrictStrategy(createTestLogger()),
	}

	result, decision, err := strategy.GetRoutableEndpoints(ctx, "test-model", healthyEndpoints, modelEndpoints)

	assert.NoError(t, err)
	assert.Equal(t, "routed", string(decision.Action))
	assert.Equal(t, 1, len(result))
	assert.Equal(t, "http://embedding", result[0].URLString)
}

func TestDiscoveryStrategy_NoCapabilityRequirementUnfiltered(t *testing.T) {
	healthyEndpoints := []*domain.Endpoint{
		{Name: "embed", URLString: "http://embedding", Capabilities: []string{"embeddings"}},
		{Name: "chat", URLString: "http://chat", Capabilities: []string{"chat"}},
	}
	modelEndpoints := []string{"http://embedding", "http://chat"}

	strategy := &DiscoveryStrategy{
		options: config.ModelRoutingStrategyOptions{
			DiscoveryRefreshOnMiss: true,
		},
		logger:         createTestLogger(),
		strictFallback: NewStrictStrategy(createTestLogger()),
	}

	result, decision, err := strategy.GetRoutableEndpoints(context.Background(), "test-model", healthyEndpoints, modelEndpoints)

	assert.NoError(t, err)
	assert.Equal(t, "routed", string(decision.Action))
	assert.Equal(t, 2, len(result))
}
