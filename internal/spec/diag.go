package spec

import "fmt"

// Diagnostic is a policy error located by YAML path and line.
type Diagnostic struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func (d Diagnostic) Error() string {
	return fmt.Sprintf("line %d: %s: %s", d.Line, d.Path, d.Message)
}
