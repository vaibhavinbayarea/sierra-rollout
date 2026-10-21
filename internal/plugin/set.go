package plugin

import (
	"errors"
	"fmt"

	"sierra-rollout/internal/registry"
)

// ErrPluginMissing means a plugin name is unknown or of the wrong kind.
var ErrPluginMissing = errors.New("plugin missing")

// Remote constructs a remote plugin implementation for a registry entry.
type Remote func(e registry.Entry) (any, error)

// Set resolves plugin names to implementations.
type Set struct {
	reg   *registry.Registry
	impls map[string]any
}

// NewSet binds every registry entry to an implementation: built-ins from
// builtins, remote entries via remote.
func NewSet(reg *registry.Registry, builtins map[string]any, remote Remote) (*Set, error) {
	s := &Set{reg: reg, impls: map[string]any{}}
	for name, impl := range builtins {
		if e, ok := reg.Get(name); ok && e.Builtin {
			s.impls[name] = impl
		}
	}
	for _, e := range reg.Entries() {
		if e.Builtin {
			continue
		}
		if remote == nil {
			return nil, fmt.Errorf("plugin %q: remote plugins not supported", e.Name)
		}
		impl, err := remote(e)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", e.Name, err)
		}
		s.impls[e.Name] = impl
	}
	return s, nil
}

func (s *Set) Ramper(name string) (Ramper, error) { return get[Ramper](s, name, registry.KindRamper) }

func (s *Set) Validator(name string) (Validator, error) {
	return get[Validator](s, name, registry.KindValidator)
}

func (s *Set) Notifier(name string) (Notifier, error) {
	return get[Notifier](s, name, registry.KindNotifier)
}

func get[T any](s *Set, name string, kind registry.Kind) (T, error) {
	var zero T
	e, ok := s.reg.Get(name)
	if !ok || e.Kind != kind {
		return zero, fmt.Errorf("%w: %s %q", ErrPluginMissing, kind, name)
	}
	impl, ok := s.impls[name].(T)
	if !ok {
		return zero, fmt.Errorf("%w: no implementation for %s %q", ErrPluginMissing, kind, name)
	}
	return impl, nil
}
