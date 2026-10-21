// Package registry loads the static plugin catalog (plugins.yaml).
package registry

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

type Kind string

const (
	KindRamper    Kind = "ramper"
	KindValidator Kind = "validator"
	KindNotifier  Kind = "notifier"
)

// Entry describes one plugin.
type Entry struct {
	Name         string
	Kind         Kind
	Builtin      bool
	Endpoint     string
	Timeout      time.Duration // 0 means the kind's default
	ParamsSchema json.RawMessage
}

type Registry struct {
	entries map[string]Entry
	schemas map[string]*jsonschema.Schema
}

//go:embed schemas/*.json
var builtinSchemas embed.FS

// builtinKinds lists the plugins the service ships in-process.
var builtinKinds = map[string]Kind{
	"prometheus":      KindValidator,
	"http-health":     KindValidator,
	"manual-approval": KindValidator,
	"mock-ramper":     KindRamper,
	"log-notifier":    KindNotifier,
}

type fileEntry struct {
	Name         string `yaml:"name"`
	Kind         Kind   `yaml:"kind"`
	Builtin      bool   `yaml:"builtin"`
	Endpoint     string `yaml:"endpoint"`
	Timeout      string `yaml:"timeout"`
	ParamsSchema string `yaml:"params_schema"`
}

// Load reads a registry file. params_schema paths are relative to it.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Plugins []fileEntry `yaml:"plugins"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("registry %s: %w", path, err)
	}

	r := &Registry{entries: map[string]Entry{}, schemas: map[string]*jsonschema.Schema{}}
	for _, fe := range f.Plugins {
		e, err := toEntry(fe, filepath.Dir(path))
		if err != nil {
			return nil, fmt.Errorf("registry plugin %q: %w", fe.Name, err)
		}
		if _, dup := r.entries[e.Name]; dup {
			return nil, fmt.Errorf("registry plugin %q: duplicate name", e.Name)
		}
		if len(e.ParamsSchema) > 0 {
			s, err := compileSchema(e.Name, e.ParamsSchema)
			if err != nil {
				return nil, fmt.Errorf("registry plugin %q: params schema: %w", e.Name, err)
			}
			r.schemas[e.Name] = s
		}
		r.entries[e.Name] = e
	}
	return r, nil
}

func toEntry(fe fileEntry, dir string) (Entry, error) {
	e := Entry{Name: fe.Name, Kind: fe.Kind, Builtin: fe.Builtin, Endpoint: fe.Endpoint}
	if e.Name == "" {
		return e, fmt.Errorf("name is required")
	}
	switch e.Kind {
	case KindRamper, KindValidator, KindNotifier:
	default:
		return e, fmt.Errorf("unknown kind %q", e.Kind)
	}
	if fe.Timeout != "" {
		d, err := time.ParseDuration(fe.Timeout)
		if err != nil {
			return e, fmt.Errorf("invalid timeout %q", fe.Timeout)
		}
		e.Timeout = d
	}
	if e.Builtin {
		kind, ok := builtinKinds[e.Name]
		if !ok {
			return e, fmt.Errorf("not a built-in plugin")
		}
		if kind != e.Kind {
			return e, fmt.Errorf("built-in is a %s, not a %s", kind, e.Kind)
		}
		b, err := builtinSchemas.ReadFile("schemas/" + e.Name + ".json")
		if err != nil {
			return e, err
		}
		e.ParamsSchema = b
		return e, nil
	}
	if e.Endpoint == "" {
		return e, fmt.Errorf("remote plugin requires endpoint")
	}
	if fe.ParamsSchema != "" {
		b, err := os.ReadFile(filepath.Join(dir, fe.ParamsSchema))
		if err != nil {
			return e, err
		}
		e.ParamsSchema = b
	}
	return e, nil
}

func compileSchema(name string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	url := "mem://" + name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

func (r *Registry) Get(name string) (Entry, bool) {
	e, ok := r.entries[name]
	return e, ok
}

// ValidateParams checks params against the plugin's schema. Plugins without
// a schema accept any params.
func (r *Registry) ValidateParams(name string, params map[string]any) error {
	if _, ok := r.entries[name]; !ok {
		return fmt.Errorf("unknown plugin %q", name)
	}
	s, ok := r.schemas[name]
	if !ok {
		return nil
	}
	if params == nil {
		params = map[string]any{}
	}
	// Round-trip through JSON so YAML-decoded values become JSON types.
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return err
	}
	return s.Validate(inst)
}
