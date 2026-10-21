// Package decision maps a step's validation verdicts to the next action.
// It is pure so all policy logic is testable without Temporal.
package decision

import (
	"fmt"
	"strings"
	"time"

	"sierra-rollout/internal/plan"
)

type NamedVerdict struct {
	Name     string
	Severity string
	Verdict  plan.Verdict
}

type Action string

const (
	Continue          Action = "CONTINUE"
	RetryInconclusive Action = "RETRY_INCONCLUSIVE"
	Fail              Action = "FAIL"
)

type Result struct {
	Action         Action
	Retry          []string // validation names to re-run
	Backoff        time.Duration
	NotifyWarnings []string // warning failures to notify about
	Reason         string
}

// Decide applies rule to vs. inconclusiveAttempt is the number of
// inconclusive retries already done for this step.
func Decide(vs []NamedVerdict, rule plan.DecisionRule, inconclusiveAttempt int) Result {
	var r Result
	var failed []string
	for _, v := range vs {
		outcome := v.Verdict.Outcome
		if outcome == plan.AWAIT_SIGNAL {
			outcome = plan.INCONCLUSIVE
		}
		if outcome == plan.INCONCLUSIVE {
			if inconclusiveAttempt < rule.Inconclusive.Retry {
				r.Retry = append(r.Retry, v.Name)
				continue
			}
			outcome = plan.PASS
			if rule.Inconclusive.Then != "pass" {
				outcome = plan.FAIL
			}
		}
		if outcome != plan.FAIL {
			continue
		}
		if v.Severity != "warning" {
			failed = append(failed, v.Name)
			continue
		}
		switch rule.Warning {
		case "fail":
			failed = append(failed, v.Name)
		case "notify":
			r.NotifyWarnings = append(r.NotifyWarnings, v.Name)
		}
	}
	switch {
	case len(failed) > 0:
		r.Action, r.Retry = Fail, nil
		r.Reason = fmt.Sprintf("failed: %s", strings.Join(failed, ", "))
	case len(r.Retry) > 0:
		r.Action, r.Backoff = RetryInconclusive, rule.Inconclusive.Backoff
		r.Reason = fmt.Sprintf("inconclusive: %s", strings.Join(r.Retry, ", "))
	default:
		r.Action = Continue
	}
	return r
}
