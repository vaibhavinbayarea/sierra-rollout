package cli

import (
	"fmt"
	"io"
	"strings"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/spec"
	"sierra-rollout/internal/workflows"
)

func RenderDiagnostics(w io.Writer, file string, ds []spec.Diagnostic) {
	for _, d := range ds {
		fmt.Fprintf(w, "%s:%d: %s: %s\n", file, d.Line, d.Path, d.Message)
	}
}

// RenderPlan prints the plan as a stage → wave → customer → region tree.
func RenderPlan(w io.Writer, p *plan.Plan) {
	fmt.Fprintf(w, "%s  %s\n", p.ID, p.Version)
	for _, s := range p.Stages {
		fmt.Fprintf(w, "%s\n", s.Name)
		for _, wv := range s.Waves {
			fmt.Fprintf(w, "  %s (max_parallel %d)\n", wv.Name, wv.MaxParallel)
			for _, c := range wv.Customers {
				fmt.Fprintf(w, "    %s\n", c.Customer)
				for _, t := range c.Targets {
					var vs []string
					for _, v := range t.Validations {
						if v.When == "final_step" {
							vs = append(vs, v.Name+" (final step)")
						} else {
							vs = append(vs, v.Name)
						}
					}
					fmt.Fprintf(w, "      %s  steps %v  bake %s  validate: %s\n", t.Region, t.Steps, t.Bake, strings.Join(vs, ", "))
				}
			}
		}
	}
}

// RenderStatus prints the rollout header and one line per target, grouped
// by stage, wave and customer in plan order.
func RenderStatus(w io.Writer, st *workflows.RolloutStatus) {
	header := fmt.Sprintf("%s  %s  %s", st.ID, st.Version, st.State)
	if st.WaitingForStage != "" {
		header += "  (waiting for stage " + st.WaitingForStage + ")"
	}
	if st.Message != "" {
		header += "  " + st.Message
	}
	fmt.Fprintln(w, header)
	var stage, wave, customer string
	for _, id := range st.Order {
		t := st.Targets[id]
		if t.Stage != stage {
			stage, wave, customer = t.Stage, "", ""
			fmt.Fprintf(w, "%s\n", stage)
		}
		if t.Wave != wave {
			wave, customer = t.Wave, ""
			fmt.Fprintf(w, "  %s\n", wave)
		}
		if t.Customer != customer {
			customer = t.Customer
			fmt.Fprintf(w, "    %s\n", customer)
		}
		line := fmt.Sprintf("      %-12s %-18s step %d/%d @ %d%%", t.Region, t.State, t.Step, t.Steps, t.Percent)
		if t.Mode == "dark" {
			line += " mirrored"
		}
		if len(t.AwaitingApproval) > 0 {
			line += "  awaiting: " + strings.Join(t.AwaitingApproval, ", ")
		}
		if t.Message != "" {
			line += "  " + t.Message
		}
		fmt.Fprintln(w, line)
	}
}
