package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// UpstreamURL is the default llama.cpp repository.
const UpstreamURL = "https://github.com/ggml-org/llama.cpp"

// UpstreamName is the registry name of the default source.
const UpstreamName = "upstream"

// Source is a registered Git repository.
type Source struct {
	Name            string     `json:"name"`
	URL             string     `json:"url"`
	Ref             string     `json:"ref,omitempty"`
	LocalCommit     string     `json:"local_commit,omitempty"`
	RemoteCommit    string     `json:"remote_commit,omitempty"`
	UpdateAvailable bool       `json:"update_available"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
}

// Manager owns the source registry and the source checkouts on disk.
type Manager struct {
	dir     string
	path    string
	git     Git
	mu      sync.Mutex
	sources map[string]*Source
}

// NewManager loads (or initializes) the registry at registryFile and
// stores checkouts under dir.
func NewManager(dir, registryFile string, git Git) (*Manager, error) {
	if git == nil {
		git = ExecGit{}
	}
	m := &Manager{dir: dir, path: registryFile, git: git, sources: map[string]*Source{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var doc struct {
		Sources []*Source `json:"sources"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse source registry: %w", err)
	}
	for _, s := range doc.Sources {
		m.sources[s.Name] = s
	}
	return nil
}

func (m *Manager) save() error {
	names := make([]string, 0, len(m.sources))
	for n := range m.sources {
		names = append(names, n)
	}
	sort.Strings(names)
	doc := struct {
		Sources []*Source `json:"sources"`
	}{}
	for _, n := range names {
		doc.Sources = append(doc.Sources, m.sources[n])
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// EnsureDefault registers the upstream source when the registry is empty.
func (m *Manager) EnsureDefault() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sources) > 0 {
		return nil
	}
	m.sources[UpstreamName] = &Source{Name: UpstreamName, URL: UpstreamURL, Ref: "master"}
	return m.save()
}

// List returns the registered sources sorted by name.
func (m *Manager) List() []Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Source, 0, len(m.sources))
	for _, s := range m.sources {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a copy of a registered source.
func (m *Manager) Get(name string) (Source, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sources[name]
	if !ok {
		return Source{}, false
	}
	return *s, true
}

// Add registers a new source. It does not touch the network.
func (m *Manager) Add(s Source) error {
	s.Name = strings.TrimSpace(s.Name)
	s.URL = strings.TrimSpace(s.URL)
	s.Ref = strings.TrimSpace(s.Ref)
	if s.Name == "" {
		return errors.New("source name is required")
	}
	if s.URL == "" {
		return errors.New("source url is required")
	}
	if strings.ContainsAny(s.Name, "/\\ ") {
		return fmt.Errorf("invalid source name %q", s.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sources[s.Name]; exists {
		return fmt.Errorf("source %q already exists", s.Name)
	}
	cp := s
	m.sources[s.Name] = &cp
	return m.save()
}

// Remove unregisters a source, optionally deleting its checkout.
func (m *Manager) Remove(name string, purge bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sources[name]; !ok {
		return fmt.Errorf("source %q not found", name)
	}
	delete(m.sources, name)
	if err := m.save(); err != nil {
		return err
	}
	if purge {
		return os.RemoveAll(m.LocalPath(name))
	}
	return nil
}

// LocalPath returns the checkout directory for a source.
func (m *Manager) LocalPath(name string) string { return filepath.Join(m.dir, name) }

// Cloned reports whether a checkout exists on disk.
func (m *Manager) Cloned(name string) bool {
	info, err := os.Stat(filepath.Join(m.LocalPath(name), ".git"))
	return err == nil && info.IsDir()
}

// Check resolves the remote ref via ls-remote and compares it with the
// local checkout. It performs no clone and no build.
func (m *Manager) Check(ctx context.Context, name string) (Source, error) {
	m.mu.Lock()
	src, ok := m.sources[name]
	if !ok {
		m.mu.Unlock()
		return Source{}, fmt.Errorf("source %q not found", name)
	}
	cp := *src
	m.mu.Unlock()

	now := time.Now().UTC()
	remote, err := ResolveRemote(ctx, m.git, cp.URL, cp.Ref)
	if err != nil {
		return m.update(name, func(s *Source) {
			s.LastError = err.Error()
			s.LastCheckedAt = &now
		}), err
	}
	local := ""
	if m.Cloned(name) {
		if head, err := m.HeadCommit(ctx, name); err == nil {
			local = head
		}
	}
	updated := m.update(name, func(s *Source) {
		s.RemoteCommit = remote
		s.LocalCommit = local
		s.UpdateAvailable = local == "" || local != remote
		s.LastError = ""
		s.LastCheckedAt = &now
	})
	return updated, nil
}

func (m *Manager) update(name string, fn func(*Source)) Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sources[name]
	if s == nil {
		return Source{}
	}
	fn(s)
	_ = m.save()
	return *s
}

// HeadCommit returns the currently checked-out commit.
func (m *Manager) HeadCommit(ctx context.Context, name string) (string, error) {
	out, err := m.git.Run(ctx, m.LocalPath(name), "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// EnsureClone clones the repository if no checkout exists yet. This is
// a network- and disk-heavy operation.
func (m *Manager) EnsureClone(ctx context.Context, name string) error {
	src, ok := m.Get(name)
	if !ok {
		return fmt.Errorf("source %q not found", name)
	}
	if m.Cloned(name) {
		return nil
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	if _, err := m.git.Run(ctx, "", "clone", "--", src.URL, m.LocalPath(name)); err != nil {
		return err
	}
	return nil
}

// EnsureCheckout fetches and checks out the given commit (detached). If
// commit is empty the configured ref is resolved remotely first.
func (m *Manager) EnsureCheckout(ctx context.Context, name, commit string) (string, error) {
	src, ok := m.Get(name)
	if !ok {
		return "", fmt.Errorf("source %q not found", name)
	}
	if err := m.EnsureClone(ctx, name); err != nil {
		return "", err
	}
	dir := m.LocalPath(name)
	if _, err := m.git.Run(ctx, dir, "fetch", "--tags", "--prune", "origin"); err != nil {
		return "", err
	}
	if commit == "" {
		commit = src.RemoteCommit
	}
	if commit == "" {
		var err error
		commit, err = ResolveRemote(ctx, m.git, src.URL, src.Ref)
		if err != nil {
			return "", err
		}
	}
	if _, err := m.git.Run(ctx, dir, "-c", "advice.detachedHead=false", "checkout", "--force", commit); err != nil {
		return "", err
	}
	head, err := m.HeadCommit(ctx, name)
	if err != nil {
		return "", err
	}
	m.update(name, func(s *Source) { s.LocalCommit = head; s.UpdateAvailable = head != s.RemoteCommit })
	return head, nil
}
