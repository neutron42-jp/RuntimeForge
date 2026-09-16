package runtimes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Manifest describes one installed runtime variant.
type Manifest struct {
	ID                     string            `json:"id"`
	Source                 string            `json:"source"`
	GitURL                 string            `json:"git_url"`
	Ref                    string            `json:"ref,omitempty"`
	Commit                 string            `json:"commit"`
	Backend                string            `json:"backend"`
	Backends               []string          `json:"backends,omitempty"`
	BuiltAt                time.Time         `json:"built_at"`
	ResolvedCMakeFlags     []string          `json:"resolved_cmake_flags"`
	Env                    []string          `json:"env,omitempty"`
	Binaries               map[string]string `json:"binaries"`
	SupportedArchitectures []string          `json:"supported_architectures"`
	HostGPUs               []string          `json:"host_gpus,omitempty"`
}

// ID builds the canonical runtime identifier.
func MakeID(source, commit, backend string) string {
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	return source + "@" + short + "/" + backend
}

// ShortCommit returns the abbreviated commit hash.
func (m Manifest) ShortCommit() string {
	if len(m.Commit) > 12 {
		return m.Commit[:12]
	}
	return m.Commit
}

// SupportsArchitecture reports whether the runtime lists arch.
func (m Manifest) SupportsArchitecture(arch string) bool {
	for _, a := range m.SupportedArchitectures {
		if a == arch {
			return true
		}
	}
	return false
}

// BackendSet returns the backends compiled into this runtime. It falls
// back to parsing the Backend label for manifests written by earlier
// versions.
func (m Manifest) BackendSet() []string {
	if len(m.Backends) > 0 {
		return m.Backends
	}
	if m.Backend == "" {
		return nil
	}
	return strings.Split(m.Backend, "+")
}

// HasBackend reports whether the runtime includes the named backend.
func (m Manifest) HasBackend(name string) bool {
	for _, b := range m.BackendSet() {
		if b == name {
			return true
		}
	}
	return false
}

// Registry reads and writes manifests on disk.
type Registry struct {
	root string
}

// NewRegistry roots a registry at the given runtimes directory.
func NewRegistry(root string) *Registry { return &Registry{root: root} }

// Root returns the runtimes directory.
func (r *Registry) Root() string { return r.root }

// ManifestPath returns the on-disk path of a variant manifest.
func (r *Registry) ManifestPath(source, commit, backend string) string {
	return filepath.Join(r.root, source, commit, backend, "manifest.json")
}

// Write persists a manifest to its canonical location.
func (r *Registry) Write(m Manifest) error {
	path := r.ManifestPath(m.Source, m.Commit, m.Backend)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get loads a runtime by id.
func (r *Registry) Get(id string) (Manifest, bool) {
	for _, m := range r.mustList() {
		if m.ID == id {
			return m, true
		}
	}
	return Manifest{}, false
}

// List returns every installed runtime, newest build first.
func (r *Registry) List() ([]Manifest, error) {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Manifest
	for _, src := range entries {
		if !src.IsDir() {
			continue
		}
		commits, err := os.ReadDir(filepath.Join(r.root, src.Name()))
		if err != nil {
			continue
		}
		for _, commit := range commits {
			if !commit.IsDir() {
				continue
			}
			backends, err := os.ReadDir(filepath.Join(r.root, src.Name(), commit.Name()))
			if err != nil {
				continue
			}
			for _, backend := range backends {
				path := filepath.Join(r.root, src.Name(), commit.Name(), backend.Name(), "manifest.json")
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				var m Manifest
				if err := json.Unmarshal(data, &m); err != nil {
					continue
				}
				if m.ID == "" {
					m.ID = MakeID(m.Source, m.Commit, m.Backend)
				}
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BuiltAt.After(out[j].BuiltAt) })
	return out, nil
}

func (r *Registry) mustList() []Manifest {
	list, _ := r.List()
	return list
}

// BinaryPath resolves a runtime binary (e.g. "llama-server") to an
// absolute path.
func (r *Registry) BinaryPath(m Manifest, name string) (string, error) {
	rel, ok := m.Binaries[name]
	if !ok {
		return "", fmt.Errorf("runtime %s has no %q binary", m.ID, name)
	}
	path := filepath.Join(r.root, m.Source, m.Commit, m.Backend, rel)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("runtime binary missing: %w", err)
	}
	return path, nil
}

// Remove deletes a runtime variant directory.
func (r *Registry) Remove(id string) error {
	m, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("runtime %q not found", id)
	}
	return os.RemoveAll(filepath.Join(r.root, m.Source, m.Commit, m.Backend))
}

// FilterByArchitecture returns the runtimes supporting arch.
func FilterByArchitecture(list []Manifest, arch string) []Manifest {
	var out []Manifest
	for _, m := range list {
		if m.SupportsArchitecture(arch) {
			out = append(out, m)
		}
	}
	return out
}

// ParseID splits an id into source and backend, ignoring the commit.
func ParseID(id string) (source, backend string) {
	at := strings.Index(id, "@")
	if at < 0 {
		return id, ""
	}
	source = id[:at]
	rest := id[at+1:]
	if slash := strings.Index(rest, "/"); slash >= 0 {
		backend = rest[slash+1:]
	}
	return source, backend
}
