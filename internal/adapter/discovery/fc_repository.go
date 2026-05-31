package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thushan/olla/internal/config"
	"github.com/thushan/olla/internal/core/constants"
	"github.com/thushan/olla/internal/core/domain"
	"github.com/thushan/olla/internal/logger"
)

// fcModelSpec mirrors the Flight Controller ModelSpec fields we care about for
// building Olla endpoint configs. Only Name and Port are required for routing.
type fcModelSpec struct {
	Name                    string   `json:"name"`
	Framework               string   `json:"framework"`
	OllaEndpointType        string   `json:"ollaEndpointType,omitempty"`
	EndpointType            string   `json:"endpointType,omitempty"`
	HealthCheckURL          string   `json:"healthCheckURL,omitempty"`
	ModelURL                string   `json:"modelURL,omitempty"`
	CircuitBreakerTimeout   string   `json:"circuitBreakerTimeout,omitempty"`
	ModelName               string   `json:"modelName,omitempty"`
	LoadedModel             string   `json:"loadedModel,omitempty"`
	Capabilities            []string `json:"capabilities,omitempty"`
	ExtraArgs               []string `json:"extraArgs,omitempty"`
	Port                    int      `json:"port"`
	Priority                int      `json:"priority,omitempty"`
	CircuitBreakerThreshold int      `json:"circuitBreakerThreshold,omitempty"`
}

// fcRegistryEntry mirrors the Flight Controller RegistryEntry type returned by
// GET /registry. We only parse the fields Olla needs; extra fields are ignored.
type fcRegistryEntry struct {
	Host   string        `json:"host"`
	Models []fcModelSpec `json:"models"`
}

// FCDiscoveryPoller polls the Flight Controller /registry endpoint and reconciles
// the StaticEndpointRepository on each poll. A full replace-on-poll strategy ensures
// that endpoints removed from the FC registry are promptly evicted from Olla's rotation,
// meeting the <30s convergence acceptance criterion for petersimmons1972/instinct#12.
//
// When constructed with NewFCDiscoveryPollerWithRegistry, the poller also pre-registers
// model names into the provided ModelRegistry on every successful poll. This eliminates
// the cold-start window (petersimmons1972/olla#306) where FC endpoints have no model
// registry entries until the 5-minute ModelDiscoveryService cycle completes, which
// previously caused a nil-pointer panic via DiscoveryRefreshOnMiss (issue #33).
type FCDiscoveryPoller struct {
	logger        logger.StyledLogger
	repo          *StaticEndpointRepository
	modelRegistry domain.ModelRegistry // optional; nil = no pre-registration
	client        *http.Client
	registryURL   string
}

// NewFCDiscoveryPoller creates a poller that syncs Olla endpoints from FC /registry.
// registryBaseURL is the FC service base URL, e.g. http://ai-fleet-controller.ai-fleet.svc.cluster.local.
func NewFCDiscoveryPoller(repo *StaticEndpointRepository, modelRegistry domain.ModelRegistry, registryBaseURL string, log logger.StyledLogger) *FCDiscoveryPoller {
	return &FCDiscoveryPoller{
		repo:          repo,
		modelRegistry: modelRegistry,
		registryURL:   registryBaseURL + "/registry",
		logger:        log,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// NewFCDiscoveryPollerWithRegistry creates a poller that syncs Olla endpoints from
// FC /registry and also pre-registers FC model names in modelRegistry on each poll.
// This eliminates the cold-start window (petersimmons1972/olla#306) by making model
// routing decisions available immediately after the first poll, rather than waiting
// for the periodic ModelDiscoveryService cycle to populate the registry.
func NewFCDiscoveryPollerWithRegistry(repo *StaticEndpointRepository, modelRegistry domain.ModelRegistry, registryBaseURL string, log logger.StyledLogger) *FCDiscoveryPoller {
	return NewFCDiscoveryPoller(repo, modelRegistry, registryBaseURL, log)
}

// Poll fetches the FC registry and reconciles Olla's endpoint set.
// On FC unreachable: logs a warning, preserves the current endpoint set (fail-open).
// On success: fully replaces the endpoint set with the FC-derived list.
// When modelRegistry is set, also pre-registers FC model names so routing works
// before ModelDiscoveryService completes its first async discovery cycle.
func (p *FCDiscoveryPoller) Poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.registryURL, nil)
	if err != nil {
		return fmt.Errorf("fc-discovery: build request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		// Fail-open: preserve the existing endpoint set when FC is unreachable.
		p.logger.Warn("fc-discovery: FC registry unreachable, preserving existing endpoints", "url", p.registryURL, "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		p.logger.Warn("fc-discovery: FC registry returned non-200, preserving existing endpoints",
			"url", p.registryURL, "status", resp.StatusCode)
		return nil
	}

	var entries []fcRegistryEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return fmt.Errorf("fc-discovery: decode registry response: %w", err)
	}

	configs := p.fcEntriesToEndpointConfigs(entries)
	if err := p.repo.LoadFromConfig(ctx, configs); err != nil {
		return fmt.Errorf("fc-discovery: load endpoint configs: %w", err)
	}
	p.logger.Info("fc-discovery: endpoint set reconciled from FC registry",
		"endpoints", len(configs))

	// Pre-register FC model names in the model registry so routing decisions can
	// be made immediately after this poll, eliminating the cold-start window where
	// compatible_endpoints=0 until ModelDiscoveryService's async cycle completes
	// (petersimmons1972/olla#306).
	if p.modelRegistry != nil {
		p.syncModelsToRegistry(ctx, entries)
	}

	return nil
}

// syncModelsToRegistry performs a full replace of FC model→endpoint mappings in
// the model registry, mirroring the replace-on-poll strategy used for endpoints.
// Each (host, port) pair maps to exactly one model name from FC; that mapping is
// cleared and re-written on every successful poll to evict stale entries.
func (p *FCDiscoveryPoller) syncModelsToRegistry(ctx context.Context, entries []fcRegistryEntry) {
	// Build the desired set of endpointURL→[]modelName from the fresh FC payload.
	// Group ALL models that share the same host:port under one URL: the model
	// registry's RegisterModels replaces the full model list for a URL, so two
	// models on the same endpoint must be registered together or the second
	// clobbers the first (petersimmons1972/olla#4).
	desiredByURL := make(map[string][]*domain.ModelInfo)
	var desiredURLOrder []string // preserve first-seen order for stable logging/registration

	for _, entry := range entries {
		for _, model := range entry.Models {
			if model.Port == 0 {
				continue
			}
			endpointURL := "http://" + net.JoinHostPort(entry.Host, strconv.Itoa(model.Port))
			if _, seen := desiredByURL[endpointURL]; !seen {
				desiredURLOrder = append(desiredURLOrder, endpointURL)
			}
			desiredByURL[endpointURL] = append(desiredByURL[endpointURL], &domain.ModelInfo{
				Name:         fcRoutingModelName(model),
				Capabilities: append([]string(nil), model.Capabilities...),
			})
		}
	}

	// Collect the current set of registered endpoints so we can evict stale ones.
	// If GetEndpointModelMap fails, we skip eviction (safe: extra stale entries
	// cause spurious "model found" decisions but never nil-panics).
	currentMap, err := p.modelRegistry.GetEndpointModelMap(ctx)
	if err != nil {
		p.logger.Warn("fc-discovery: could not fetch current model map for eviction, skipping stale cleanup", "error", err)
	} else {
		for url := range currentMap {
			if _, ok := desiredByURL[url]; !ok {
				if err := p.modelRegistry.RemoveEndpoint(ctx, url); err != nil {
					p.logger.Warn("fc-discovery: failed to evict stale model endpoint", "url", url, "error", err)
				}
			}
		}
	}

	// Register/refresh the desired endpoint→model mappings, one call per unique
	// URL with the FULL model list so co-located models do not clobber each other.
	for _, url := range desiredURLOrder {
		if err := p.modelRegistry.RegisterModels(ctx, url, desiredByURL[url]); err != nil {
			p.logger.Warn("fc-discovery: failed to pre-register models", "url", url, "error", err)
		}
	}

	p.logger.Debug("fc-discovery: pre-registered FC models in model registry",
		"endpoints", len(desiredURLOrder))
}

// fcRoutingModelName returns the model name to use for routing and registry keying.
// Priority: modelName (FC-assigned canonical name) > loadedModel (runtime model name)
// > --alias from extraArgs (llama-cpp serving alias) > service name (FC CRD name,
// which may be a deployment alias like "embed-mi50").
func fcRoutingModelName(model fcModelSpec) string {
	if model.ModelName != "" {
		return model.ModelName
	}
	if model.LoadedModel != "" {
		return model.LoadedModel
	}
	// Extract --alias from extraArgs for frameworks like llama-cpp that expose a
	// serving alias different from the service name (e.g. BAAI/bge-m3 vs embed-w6800).
	for i, arg := range model.ExtraArgs {
		if arg == "--alias" && i+1 < len(model.ExtraArgs) {
			return model.ExtraArgs[i+1]
		}
	}
	return model.Name
}

// RunLoop starts a background polling loop. It polls immediately on first call, then
// at the configured interval. The loop exits when ctx is cancelled.
func (p *FCDiscoveryPoller) RunLoop(ctx context.Context, interval time.Duration) {
	// Immediate first poll so Olla is populated before the ticker fires.
	if err := p.Poll(ctx); err != nil {
		p.logger.Warn("fc-discovery: initial poll error", "error", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("fc-discovery: loop stopped")
			return
		case <-ticker.C:
			if err := p.Poll(ctx); err != nil {
				p.logger.Warn("fc-discovery: poll error", "error", err)
			}
		}
	}
}

// fcEntriesToEndpointConfigs converts FC registry entries into Olla EndpointConfig slices.
// Each (host, model) pair becomes one endpoint at http://<host>:<port>.
// Type and optional endpoint tuning are preserved from FC when present. Framework
// defaults keep older FC registries useful while allowing richer metadata to make
// FC and static discovery produce equivalent routing behaviour.
func (p *FCDiscoveryPoller) fcEntriesToEndpointConfigs(entries []fcRegistryEntry) []config.EndpointConfig {
	defaultPriority := 100
	var configs []config.EndpointConfig

	for _, entry := range entries {
		for _, model := range entry.Models {
			if model.Port == 0 {
				continue // skip models without a port (incomplete CRD)
			}
			endpointType, ok := fcEndpointType(model)
			if !ok {
				p.logger.Warn("fc-discovery: skipping endpoint with unsupported endpoint type",
					"host", entry.Host,
					"model", model.Name,
					"framework", model.Framework,
					"endpoint_type", model.EndpointType,
					"olla_endpoint_type", model.OllaEndpointType)
				continue
			}
			priority := defaultPriority
			if model.Priority != 0 {
				priority = model.Priority
			}
			cbTimeout, _ := time.ParseDuration(model.CircuitBreakerTimeout)
			cfg := config.EndpointConfig{
				URL:                     "http://" + net.JoinHostPort(entry.Host, strconv.Itoa(model.Port)),
				Name:                    fmt.Sprintf("%s-%s", entry.Host, model.Name),
				Type:                    endpointType,
				Priority:                &priority,
				Capabilities:            model.Capabilities,
				HealthCheckURL:          model.HealthCheckURL,
				ModelURL:                model.ModelURL,
				CircuitBreakerTimeout:   cbTimeout,
				CircuitBreakerThreshold: model.CircuitBreakerThreshold,
			}
			configs = append(configs, cfg)
		}
	}
	return configs
}

func fcEndpointType(model fcModelSpec) (string, bool) {
	if model.OllaEndpointType != "" {
		return model.OllaEndpointType, true
	}
	if model.EndpointType != "" {
		return model.EndpointType, true
	}

	switch strings.ToLower(model.Framework) {
	case "":
		return constants.ProviderTypeOpenAI, true
	case constants.ProviderTypeVLLM:
		return constants.ProviderTypeVLLM, true
	case constants.ProviderTypeOllama:
		return constants.ProviderTypeOllama, true
	case constants.ProviderTypeLlamaCpp, "llama-cpp", "llama_cpp":
		return constants.ProviderTypeLlamaCpp, true
	case "infinity":
		return constants.ProviderTypeOpenAI, true
	default:
		return "", false
	}
}
