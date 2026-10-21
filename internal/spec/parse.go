package spec

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

var lineRe = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)

// Parse decodes a policy, rejecting unknown fields. On any diagnostic the
// returned policy may be nil or partial and must not be used.
func Parse(data []byte) (*Policy, *Lines, []Diagnostic) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, []Diagnostic{toDiag(err.Error(), nil)}
	}
	lines := buildLines(&root)

	var p Policy
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		var te *yaml.TypeError
		if !errors.As(err, &te) {
			return nil, lines, []Diagnostic{toDiag(err.Error(), lines)}
		}
		diags := make([]Diagnostic, 0, len(te.Errors))
		for _, msg := range te.Errors {
			diags = append(diags, toDiag(msg, lines))
		}
		return &p, lines, diags
	}
	return &p, lines, nil
}

func toDiag(msg string, lines *Lines) Diagnostic {
	d := Diagnostic{Line: 1, Message: msg}
	if m := lineRe.FindStringSubmatch(msg); m != nil {
		d.Line, _ = strconv.Atoi(m[1])
		d.Message = m[2]
		d.Path = lines.pathAt(d.Line)
	}
	return d
}
