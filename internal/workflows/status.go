package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"

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
	// Seq increases with every update from a target, so the global can
	// ignore updates delivered out of order.
	Seq int `json:"seq"`
}

var severity = map[string]int{Succeeded: 0, Held: 1, Aborted: 2}

// Worst returns the more severe status (ABORTED > HELD > SUCCEEDED).
func Worst(a, b string) string {
	if severity[b] > severity[a] {
		return b
	}
	return a
}

// QueryStatus is the GlobalRollout query returning RolloutStatus.
const QueryStatus = "status"

// Rollout-level states besides the terminal statuses.
const (
	StateRunning         = "running"
	StateWaitingForStage = "waiting_for_stage"
)

type RolloutStatus struct {
	ID              string                  `json:"id"`
	Agent           string                  `json:"agent"`
	Version         string                  `json:"version"`
	State           string                  `json:"state"` // running | waiting_for_stage | SUCCEEDED | HELD | ABORTED
	WaitingForStage string                  `json:"waiting_for_stage,omitempty"`
	Order           []string                `json:"order"`
	Targets         map[string]TargetStatus `json:"targets"`
	Message         string                  `json:"message,omitempty"`
}

// applyProgress records ts unless a newer update for the target was
// already applied.
func applyProgress(st *RolloutStatus, ts TargetStatus) {
	if cur, ok := st.Targets[ts.ID]; ok && cur.Seq >= ts.Seq && cur.Seq > 0 {
		return
	}
	st.Targets[ts.ID] = ts
}

// drainProgress applies every progress signal already buffered on c.
func drainProgress(c workflow.ReceiveChannel, st *RolloutStatus) {
	for {
		var ts TargetStatus
		if !c.ReceiveAsync(&ts) {
			return
		}
		applyProgress(st, ts)
	}
}
