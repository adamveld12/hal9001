package auth

import (
	"fmt"
	"sort"
)

// Registry maps module names to compiled-in modules.
type Registry struct {
	modules map[string]Module
}

func NewRegistry() *Registry { return &Registry{modules: map[string]Module{}} }

// Register adds m under m.Name(); duplicate names are a programming error.
func (r *Registry) Register(m Module) error {
	if m.Name() == "" {
		return fmt.Errorf("auth: module with empty name")
	}
	if _, dup := r.modules[m.Name()]; dup {
		return fmt.Errorf("auth: module %q already registered", m.Name())
	}
	r.modules[m.Name()] = m
	return nil
}

func (r *Registry) Get(name string) (Module, bool) {
	m, ok := r.modules[name]
	return m, ok
}

// Names returns the registered module names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.modules))
	for n := range r.modules {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
