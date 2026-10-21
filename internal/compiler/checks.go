package compiler

import (
	"sierra-rollout/internal/registry"
	"sierra-rollout/internal/spec"
)

// check returns semantic diagnostics for a parsed policy.
func check(p *spec.Policy, lines *spec.Lines, reg *registry.Registry) []spec.Diagnostic {
	return nil
}
