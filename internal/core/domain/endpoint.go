package domain

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

const (
	StatusStringHealthy   = "healthy"
	StatusStringBusy      = "busy"
	StatusStringOffline   = "offline"
	StatusStringWarming   = "warming"
	StatusStringUnhealthy = "unhealthy"
	StatusStringUnknown   = "unknown"
)

type Endpoint struct {
	LastChecked             time.Time
	NextCheckTime           time.Time
	URL                     *url.URL
	HealthCheckURL          *url.URL
	ModelUrl                *url.URL
	ModelFilter             *FilterConfig
	OutboundAuth            *OutboundAuth
	Name                    string
	Type                    string `json:"type,omitempty"`
	APIKey                  string `json:"-"`
	Status                  EndpointStatus
	URLString               string
	HealthCheckPathString   string
	HealthCheckURLString    string
	ModelURLString          string
	Capabilities            []string
	LastLatency             time.Duration
	CheckInterval           time.Duration
	CheckTimeout            time.Duration
	CircuitBreakerTimeout   time.Duration
	Priority                int
	CircuitBreakerThreshold int
	ConsecutiveFailures     int
	BackoffMultiplier       int
	PreservePath            bool
}

// OutboundAuth defines a single static header injected on requests proxied to an
// endpoint. It is an alternative to APIKey/Bearer for upstreams that authenticate
// via a custom header (e.g. "X-Api-Key"). Value is resolved with os.ExpandEnv at
// config-load time so secrets can be sourced from the environment, exactly like
// APIKey. An endpoint may set APIKey OR OutboundAuth, never both (enforced at load).
type OutboundAuth struct {
	Header string
	Value  string `json:"-"`
}

func (e *Endpoint) GetURLString() string {
	return e.URLString
}

func (e *Endpoint) GetHealthCheckURLString() string {
	return e.HealthCheckURLString
}

type EndpointStatus string

const (
	StatusHealthy   EndpointStatus = StatusStringHealthy
	StatusBusy      EndpointStatus = StatusStringBusy
	StatusOffline   EndpointStatus = StatusStringOffline
	StatusWarming   EndpointStatus = StatusStringWarming
	StatusUnhealthy EndpointStatus = StatusStringUnhealthy
	StatusUnknown   EndpointStatus = StatusStringUnknown
)

func (s EndpointStatus) IsRoutable() bool {
	switch s {
	case StatusHealthy, StatusBusy, StatusWarming:
		return true
	default:
		return false
	}
}

func (s EndpointStatus) GetTrafficWeight() float64 {
	switch s {
	case StatusHealthy:
		return 1.0
	case StatusBusy:
		return 0.3
	case StatusWarming:
		return 0.1
	default:
		return 0.0
	}
}

func (s EndpointStatus) String() string {
	return string(s)
}

type EndpointNotFoundError struct {
	URL string
}

func (e *EndpointNotFoundError) Error() string {
	return fmt.Sprintf("endpoint not found: %s", e.URL)
}

type EndpointRepository interface {
	GetAll(ctx context.Context) ([]*Endpoint, error)
	GetRoutable(ctx context.Context) ([]*Endpoint, error)
	GetHealthy(ctx context.Context) ([]*Endpoint, error)
	UpdateEndpoint(ctx context.Context, endpoint *Endpoint) error
	Exists(ctx context.Context, endpointURL *url.URL) bool
}

type EndpointSelector interface {
	Select(ctx context.Context, endpoints []*Endpoint) (*Endpoint, error)
	Name() string
	IncrementConnections(endpoint *Endpoint)
	DecrementConnections(endpoint *Endpoint)
}
