package spec

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Lines maps YAML paths (e.g. "stages[2].waves[1].steps") to source lines.
type Lines struct {
	byPath map[string]int
	byLine map[int]string // shortest path starting on each line
}

// Line returns the line of path, or 0 if unknown.
func (l *Lines) Line(path string) int {
	if l == nil {
		return 0
	}
	return l.byPath[path]
}

// pathAt returns the shortest path that starts on line, or "".
func (l *Lines) pathAt(line int) string {
	if l == nil {
		return ""
	}
	return l.byLine[line]
}

func buildLines(root *yaml.Node) *Lines {
	l := &Lines{byPath: map[string]int{}, byLine: map[int]string{}}
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		l.walk(root.Content[0], "")
	}
	return l
}

func (l *Lines) record(path string, line int) {
	l.byPath[path] = line
	if cur, ok := l.byLine[line]; !ok || len(path) < len(cur) || (len(path) == len(cur) && path < cur) {
		l.byLine[line] = path
	}
}

func (l *Lines) walk(n *yaml.Node, path string) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := n.Content[i].Value
			if path != "" {
				p = path + "." + p
			}
			l.record(p, n.Content[i].Line)
			l.walk(n.Content[i+1], p)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			p := fmt.Sprintf("%s[%d]", path, i)
			l.record(p, c.Line)
			l.walk(c, p)
		}
	}
}
