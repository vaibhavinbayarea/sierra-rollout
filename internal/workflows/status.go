package workflows

import (
	"time"

	"sierra-rollout/internal/plan"
)

// Target states.
const (
	StatePending          = "pending"
	StateRamping          = "ramping"
	StateBaking           = "baking"
	StateValidating       = "validating"
	StateAwaitingApproval = "awaiting_approval"
	StateRetrying         = "retrying"
	StatePaused           = "paused"
	StateSucceeded        = "succeeded"
	StateHeld             = "held"
	StateAborted          = "aborted"
)

// Terminal statuses for targets, stages and rollouts.
const (
	Succeeded = "SUCCEEDED"
	Held      = "HELD"
	Aborted   = "ABORTED"
)

type TargetStatus struct {
	ID               string                  `json:"id"`
	Stage            string                  `json:"stage"`
	Wave             string                  `json:"wave"`
	Customer         string                  `json:"customer"`
	Region           string                  `json:"region"`
	State            string                  `json:"state"`
	Step             int                     `json:"step"`
	Steps            int                     `json:"steps"`
	Percent          int                     `json:"percent"`
	Verdicts         map[string]plan.Verdict `json:"verdicts,omitempty"`
	AwaitingApproval []string                `json:"awaiting_approval,omitempty"`
	Message          string                  `json:"message,omitempty"`
	At               time.Time               `json:"at"`
}

var severity = map[string]int{Succeeded: 0, Held: 1, Aborted: 2}

// Worst returns the more severe status (ABORTED > HELD > SUCCEEDED).
func Worst(a, b string) string {
	if severity[b] > severity[a] {
		return b
	}
	return a
}
