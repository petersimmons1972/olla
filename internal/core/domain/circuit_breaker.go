package domain

import "time"

type CircuitBreakerState struct {
	LastTripTimestamp    *time.Time `json:"last_trip_ts,omitempty"`
	State                string     `json:"state"`
	ConsecutiveFailures  int64      `json:"consecutive_failures"`
	CooldownRemainingSec int        `json:"cooldown_remaining_s"`
}
