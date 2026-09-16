// Package appdir resolves the RuntimeForge application root and its
// well-known subdirectories. Everything the application persists lives
// under a single root (SPEC §5).
package appdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvHome overrides the application root when set.
const EnvHome = "RUNTIMEFORGE_HOME"

// Paths holds the resolved layout of the application root.
type Paths struct {
	Root     string
	Config   string // config.toml
	ConfigD  string // config.d/
	Sources  string
	Runtimes string
	Models   string
	State    string
	Logs     string
	Cache    string
}

// Home returns the application root, honoring RUNTIMEFORGE_HOME.
// A leading "~" is expanded to the user's home directory.
func Home() (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvHome)); v != "" {
		return Expand(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".runtimeforge"), nil
}

// New builds a Paths layout rooted at root.
func New(root string) Paths {
	return Paths{
		Root:     root,
		Config:   filepath.Join(root, "config.toml"),
		ConfigD:  filepath.Join(root, "config.d"),
		Sources:  filepath.Join(root, "sources"),
		Runtimes: filepath.Join(root, "runtimes"),
		Models:   filepath.Join(root, "models"),
		State:    filepath.Join(root, "state"),
		Logs:     filepath.Join(root, "logs"),
		Cache:    filepath.Join(root, "cache"),
	}
}

// Resolve determines the layout from the environment.
func Resolve() (Paths, error) {
	root, err := Home()
	if err != nil {
		return Paths{}, err
	}
	return New(root), nil
}

// Ensure creates every directory in the layout.
func (p Paths) Ensure() error {
	for _, dir := range []string{
		p.Root, p.ConfigD, p.Sources, p.Runtimes,
		p.Models, p.State, p.Logs, p.Cache,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// StateFile returns the path of a file inside the state directory.
func (p Paths) StateFile(name string) string { return filepath.Join(p.State, name) }

// Expand resolves a leading "~" against the user's home directory.
func Expand(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// ExpandAll expands every path in the slice.
func ExpandAll(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		expanded, err := Expand(p)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}
