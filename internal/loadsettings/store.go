// Package loadsettings persists per-model llama-server launch parameters
// (context size, KV cache quantization, GPU/CPU offload, threads), keyed
// by the stable model id (SPEC §12).
package loadsettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"runtimeforge/internal/supervisor"
)

// Store is a persisted map of model id -> parameters.
type Store struct {
	path string

	mu   sync.Mutex
	data map[string]supervisor.Params
}

// NewStore loads (or initializes) the settings file.
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]supervisor.Params{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("parse model settings: %w", err)
	}
	if s.data == nil {
		s.data = map[string]supervisor.Params{}
	}
	return s, nil
}

// Get returns the saved parameters for a model (zero value if none).
func (s *Store) Get(id string) supervisor.Params {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[id]
}

// Has reports whether a model has saved parameters.
func (s *Store) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[id]
	return ok
}

// Set stores parameters for a model.
func (s *Store) Set(id string, p supervisor.Params) error {
	if id == "" {
		return errors.New("model id is required")
	}
	s.mu.Lock()
	s.data[id] = p
	s.mu.Unlock()
	return s.save()
}

// Delete removes a model's saved parameters.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	delete(s.data, id)
	s.mu.Unlock()
	return s.save()
}

// All returns a copy of every saved entry.
func (s *Store) All() map[string]supervisor.Params {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]supervisor.Params, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

func (s *Store) save() error {
	s.mu.Lock()
	raw, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Merge overlays non-zero fields of over onto base.
func Merge(base, over supervisor.Params) supervisor.Params {
	out := base
	if over.ContextSize != 0 {
		out.ContextSize = over.ContextSize
	}
	if over.KVCacheTypeK != "" {
		out.KVCacheTypeK = over.KVCacheTypeK
	}
	if over.KVCacheTypeV != "" {
		out.KVCacheTypeV = over.KVCacheTypeV
	}
	if over.GPULayers != "" {
		out.GPULayers = over.GPULayers
	}
	if over.Threads != 0 {
		out.Threads = over.Threads
	}
	if over.CPURange != "" {
		out.CPURange = over.CPURange
	}
	if len(over.ExtraArgs) > 0 {
		out.ExtraArgs = append(append([]string(nil), base.ExtraArgs...), over.ExtraArgs...)
	}
	return out
}
