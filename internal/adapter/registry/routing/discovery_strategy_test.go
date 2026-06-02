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

// TestDiscoveryStrategy_CapabilityFilterReappliedAfterDiscoveryRefresh is the
// regression test for #42: when an embeddings request triggers a discovery refresh
// (model not found on current healthy set), the post-refresh "all" fallback must
// still apply the capability filter so non-embedding endpoints are excluded.
// Before the fix, updatedHealthy was returned raw, allowing non-embedding endpoints
// to leak through to the caller.
func TestDiscoveryStrategy_CapabilityFilterReappliedAfterDiscoveryRefresh(t *testing.T) {
	ctx := context.WithValue(context.Background(), constants.ContextModelCapabilitiesKey, &domain.ModelCapabilities{
		Embeddings: true,
	})

	// Pre-refresh: no healthy endpoints have the requested model.
	healthyEndpoints := []*domain.Endpoint{
		{Name: "embed", URLString: "http://embed", Capabilities: []string{"embeddings"}},
		{Name: "chat", URLString: "http://chat", Capabilities: []string{"chat"}},
	}
	modelEndpoints := []string{} // model not found yet

	// Post-refresh: discovery returns the same two endpoints (embed + chat).
	mockDiscovery := &mockDiscoveryForTest{
		healthyEndpoints: healthyEndpoints,
	}

	strategy := &DiscoveryStrategy{
		discovery: mockDiscovery,
		options: config.ModelRoutingStrategyOptions{
			FallbackBehavior:       constants.FallbackBehaviorAll,
			DiscoveryRefreshOnMiss: true,
		},
		logger:         createTestLogger(),
		strictFallback: NewStrictStrategy(createTestLogger()),
	}

	result, decision, err := strategy.GetRoutableEndpoints(ctx, "BAAI/bge-m3", healthyEndpoints, modelEndpoints)

	// Must succeed and return ONLY the embeddings-capable endpoint.
	assert.NoError(t, err)
	assert.Equal(t, "fallback", string(decision.Action))
	assert.Len(t, result, 1, "post-refresh fallback must exclude non-embedding endpoints")
	assert.Equal(t, "http://embed", result[0].URLString)
}

// TestDiscoveryStrategy_CapabilityFilterReappliedAfterDiscoveryRefresh_Reject verifies
// that when all post-refresh endpoints lack the embeddings capability, the strategy
// rejects with RoutingReasonNoCapableEndpoint rather than returning an incapable endpoint.
func TestDiscoveryStrategy_CapabilityFilterReappliedAfterDiscoveryRefresh_Reject(t *testing.T) {
	ctx := context.WithValue(context.Background(), constants.ContextModelCapabilitiesKey, &domain.ModelCapabilities{
		Embeddings: true,
	})

	// All endpoints are chat-only; none can serve embeddings.
	chatOnlyEndpoints := []*domain.Endpoint{
		{Name: "chat1", URLString: "http://chat1", Capabilities: []string{"chat"}},
		{Name: "chat2", URLString: "http://chat2", Capabilities: []string{"chat"}},
	}
	modelEndpoints := []string{}

	mockDiscovery := &mockDiscoveryForTest{
		healthyEndpoints: chatOnlyEndpoints,
	}

	strategy := &DiscoveryStrategy{
		discovery: mockDiscovery,
		options: config.ModelRoutingStrategyOptions{
			FallbackBehavior:       constants.FallbackBehaviorAll,
			DiscoveryRefreshOnMiss: true,
		},
		logger:         createTestLogger(),
		strictFallback: NewStrictStrategy(createTestLogger()),
	}

	result, decision, err := strategy.GetRoutableEndpoints(ctx, "BAAI/bge-m3", chatOnlyEndpoints, modelEndpoints)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, "rejected", string(decision.Action))
	assert.Equal(t, constants.RoutingReasonNoCapableEndpoint, decision.Reason)
}

// TestDiscoveryStrategy_EmbeddingsNoCapableEndpoint verifies the 503 path: an
// embeddings request where ALL healthy endpoints lack the "embeddings" capability
// is rejected with RoutingReasonNoCapableEndpoint and a non-nil error.
func TestDiscoveryStrategy_EmbeddingsNoCapableEndpoint(t *testing.T) {
	ctx := context.WithValue(context.Background(), constants.ContextModelCapabilitiesKey, &domain.ModelCapabilities{
		Embeddings: true,
	})

	healthyEndpoints := []*domain.Endpoint{
		{Name: "chat", URLString: "http://chat", Capabilities: []string{"chat"}},
		{Name: "vision", URLString: "http://vision", Capabilities: []string{"vision"}},
	}
	modelEndpoints := []string{"http://chat", "http://vision"}

	strategy := &DiscoveryStrategy{
		options: config.ModelRoutingStrategyOptions{
			DiscoveryRefreshOnMiss: true,
		},
		logger:         createTestLogger(),
		strictFallback: NewStrictStrategy(createTestLogger()),
	}

	result, decision, err := strategy.GetRoutableEndpoints(ctx, "test-model", healthyEndpoints, modelEndpoints)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.NotNil(t, decision)
	assert.Equal(t, "rejected", string(decision.Action))
	assert.Equal(t, constants.RoutingReasonNoCapableEndpoint, decision.Reason)
}
