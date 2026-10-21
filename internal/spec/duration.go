package spec

import (
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration written in YAML as "5m", "24h", "0s".
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return typeErr(n, "invalid duration %q", n.Value)
	}
	*d = Duration(v)
	return nil
}
