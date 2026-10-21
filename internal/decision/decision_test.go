package decision

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sierra-rollout/internal/plan"
)

func nv(name, sev string, o plan.Outcome) NamedVerdict {
	return NamedVerdict{Name: name, Severity: sev, Verdict: plan.Verdict{Outcome: o}}
}

func rule(warning, then string) plan.DecisionRule {
	return plan.DecisionRule{Critical: "all_pass", Warning: warning,
		Inconclusive: plan.InconclusiveRule{Retry: 3, Backoff: time.Minute, Then: then}}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name    string
		vs      []NamedVerdict
		rule    plan.DecisionRule
		attempt int
		action  Action
		retry   []string
		notify  []string
	}{
		{"empty", nil, rule("notify", "fail"), 0, Continue, nil, nil},
		{"all pass", []NamedVerdict{nv("a", "critical", plan.PASS), nv("b", "warning", plan.PASS)}, rule("notify", "fail"), 0, Continue, nil, nil},
		{"critical fail", []NamedVerdict{nv("a", "critical", plan.FAIL)}, rule("notify", "fail"), 0, Fail, nil, nil},
		{"warning fail notify", []NamedVerdict{nv("csat", "warning", plan.FAIL)}, rule("notify", "fail"), 0, Continue, nil, []string{"csat"}},
		{"warning fail ignore", []NamedVerdict{nv("csat", "warning", plan.FAIL)}, rule("ignore", "fail"), 0, Continue, nil, nil},
		{"warning fail fail", []NamedVerdict{nv("csat", "warning", plan.FAIL)}, rule("fail", "fail"), 0, Fail, nil, nil},
		{"inconclusive retries", []NamedVerdict{nv("error-rate", "critical", plan.INCONCLUSIVE)}, rule("notify", "fail"), 0, RetryInconclusive, []string{"error-rate"}, nil},
		{"inconclusive exhausted fail", []NamedVerdict{nv("error-rate", "critical", plan.INCONCLUSIVE)}, rule("notify", "fail"), 3, Fail, nil, nil},
		{"inconclusive exhausted pass", []NamedVerdict{nv("error-rate", "critical", plan.INCONCLUSIVE)}, rule("notify", "pass"), 3, Continue, nil, nil},
		{"fail beats retry", []NamedVerdict{nv("a", "critical", plan.FAIL), nv("b", "critical", plan.INCONCLUSIVE)}, rule("notify", "fail"), 0, Fail, nil, nil},
		{"await signal is inconclusive", []NamedVerdict{nv("signoff", "critical", plan.AWAIT_SIGNAL)}, rule("notify", "fail"), 0, RetryInconclusive, []string{"signoff"}, nil},
		{"warning inconclusive exhausted notifies", []NamedVerdict{nv("csat", "warning", plan.INCONCLUSIVE)}, rule("notify", "fail"), 3, Continue, nil, []string{"csat"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Decide(c.vs, c.rule, c.attempt)
			assert.Equal(t, c.action, r.Action)
			assert.Equal(t, c.retry, r.Retry)
			assert.Equal(t, c.notify, r.NotifyWarnings)
			if c.action == RetryInconclusive {
				assert.Equal(t, time.Minute, r.Backoff)
			}
			if c.action != Continue {
				assert.NotEmpty(t, r.Reason)
			}
		})
	}
}
