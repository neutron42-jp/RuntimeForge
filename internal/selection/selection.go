// Package select chooses which built runtime should serve a model,
// combining user overrides with automatic architecture and backend
// matching (SPEC §11).
package selection

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"runtimeforge/internal/runtimes"
)

// Selections holds the manual overrides. Formats applies to every model
// of a given format (e.g. "gguf"); Models overrides a single model.
type Selections struct {
	Formats map[string]string `json:"formats"`
	Models  map[string]string `json:"models"`
}

// Store persists selections to disk.
type Store struct {
	path string
	data Selections
}

// NewStore loads (or initializes) the selection store.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, data: Selections{Formats: map[string]string{}, Models: map[string]string{}}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return nil, fmt.Errorf("parse selections: %w", err)
	}
	if s.data.Formats == nil {
		s.data.Formats = map[string]string{}
	}
	if s.data.Models == nil {
		s.data.Models = map[string]string{}
	}
	return s, nil
}

// Get returns a copy of the current selections.
func (s *Store) Get() Selections {
	out := Selections{Formats: map[string]string{}, Models: map[string]string{}}
	for k, v := range s.data.Formats {
		out.Formats[k] = v
	}
	for k, v := range s.data.Models {
		out.Models[k] = v
	}
	return out
}

// SetFormat pins every model of a format to a runtime id (empty clears).
func (s *Store) SetFormat(format, runtimeID string) error {
	if format == "" {
		return errors.New("format is required")
	}
	if runtimeID == "" {
		delete(s.data.Formats, format)
	} else {
		s.data.Formats[format] = runtimeID
	}
	return s.save()
}

// SetModel pins a single model to a runtime id (empty clears).
func (s *Store) SetModel(modelID, runtimeID string) error {
	if modelID == "" {
		return errors.New("model id is required")
	}
	if runtimeID == "" {
		delete(s.data.Models, modelID)
	} else {
		s.data.Models[modelID] = runtimeID
	}
	return s.save()
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ModelRef identifies the model being resolved.
type ModelRef struct {
	ID           string
	Architecture string
	Format       string // defaults to "gguf"
}

// Result is the outcome of a resolution.
type Result struct {
	RuntimeID string `json:"runtime_id"`
	Backend   string `json:"backend"`
	Arch      string `json:"arch"`
	Reason    string `json:"reason"`
	Auto      bool   `json:"auto"`
}

// Resolver resolves a model to a runtime.
type Resolver struct {
	store *Store
}

// NewResolver builds a resolver over a selection store. A nil store is
// allowed and disables overrides.
func NewResolver(store *Store) *Resolver { return &Resolver{store: store} }

// Options tune automatic resolution.
type Options struct {
	BackendPriority []string
	Available       []string // backends available on this host
	AutoSelect      bool
}

// Resolve applies the priority chain: model override, format override,
// then automatic selection by architecture and backend priority.
func (r *Resolver) Resolve(ref ModelRef, list []runtimes.Manifest, opts Options) (Result, error) {
	if ref.Format == "" {
		ref.Format = "gguf"
	}

	if r.store != nil {
		sel := r.store.Get()
		if id, ok := sel.Models[ref.ID]; ok {
			return r.manual(id, ref, list, "model override")
		}
		if id, ok := sel.Formats[ref.Format]; ok {
			return r.manual(id, ref, list, "format override")
		}
	}

	if !opts.AutoSelect {
		return Result{}, fmt.Errorf("automatic selection is disabled and no override is set for %q", ref.ID)
	}

	arch := ref.Architecture
	if arch == "" {
		return Result{}, errors.New("model architecture is unknown; cannot select a runtime automatically")
	}

	candidates := runtimes.FilterByArchitecture(list, arch)
	if len(candidates) == 0 {
		return Result{}, fmt.Errorf("no built runtime supports architecture %q; add or build a source that does", arch)
	}
	candidates = filterAvailable(candidates, opts.Available)
	if len(candidates) == 0 {
		return Result{}, fmt.Errorf("runtimes support %q but none target an available backend", arch)
	}

	sortCandidates(candidates, opts.BackendPriority, opts.Available)
	best := candidates[0]
	return Result{
		RuntimeID: best.ID,
		Backend:   best.Backend,
		Arch:      arch,
		Reason:    fmt.Sprintf("auto: arch=%s backend=%s", arch, best.Backend),
		Auto:      true,
	}, nil
}

func (r *Resolver) manual(id string, ref ModelRef, list []runtimes.Manifest, reason string) (Result, error) {
	for _, m := range list {
		if m.ID == id {
			return Result{
				RuntimeID: m.ID,
				Backend:   m.Backend,
				Arch:      ref.Architecture,
				Reason:    reason,
			}, nil
		}
	}
	return Result{}, fmt.Errorf("selected runtime %q (%s) is not installed", id, reason)
}

// filterAvailable keeps runtimes that provide at least one available
// backend. An empty availability list disables the filter.
func filterAvailable(list []runtimes.Manifest, available []string) []runtimes.Manifest {
	if len(available) == 0 {
		return list
	}
	set := map[string]bool{}
	for _, b := range available {
		set[b] = true
	}
	var out []runtimes.Manifest
	for _, m := range list {
		for _, b := range m.BackendSet() {
			if set[b] {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// backendRank returns the best (lowest) priority index among a
// runtime's available backends, and whether any matched.
func backendRank(m runtimes.Manifest, rank map[string]int, available map[string]bool) (int, bool) {
	best := -1
	for _, b := range m.BackendSet() {
		if len(available) > 0 && !available[b] {
			continue
		}
		r, ok := rank[b]
		if !ok {
			r = len(rank) + 1
		}
		if best < 0 || r < best {
			best = r
		}
	}
	return best, best >= 0
}

// sortCandidates orders by the best available backend priority, then by
// newest build.
func sortCandidates(list []runtimes.Manifest, priority, available []string) {
	rank := map[string]int{}
	for i, b := range priority {
		if _, ok := rank[b]; !ok {
			rank[b] = i
		}
	}
	avail := map[string]bool{}
	for _, b := range available {
		avail[b] = true
	}
	sort.SliceStable(list, func(i, j int) bool {
		ri, _ := backendRank(list[i], rank, avail)
		rj, _ := backendRank(list[j], rank, avail)
		if ri != rj {
			return ri < rj
		}
		return list[i].BuiltAt.After(list[j].BuiltAt)
	})
}
