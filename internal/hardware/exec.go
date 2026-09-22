package hardware

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// CommandRunner abstracts external command execution so detection can be
// unit tested without touching the host.
type CommandRunner interface {
	// LookPath resolves an executable name to a path.
	LookPath(file string) (string, error)
	// Run executes a command and returns its combined stdout.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the production CommandRunner.
type ExecRunner struct{}

// LookPath implements CommandRunner. It resolves via PATH and falls back
// to well-known install locations.
func (ExecRunner) LookPath(file string) (string, error) { return LookPath(file) }

// Run implements CommandRunner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(out) == 0 {
			// Some tools write version info to stderr; prefer it.
			return ee.Stderr, err
		}
		return out, err
	}
	return out, nil
}

// LookPath resolves file on PATH, then in well-known tool directories.
// The systemd user service may not inherit the login PATH, so CUDA (and
// other tools installed outside the default PATH) would otherwise appear
// missing until the service is restarted after login.
func LookPath(file string) (string, error) {
	if p, err := exec.LookPath(file); err == nil {
		return p, nil
	}
	for _, dir := range ToolDirs() {
		cand := filepath.Join(dir, file)
		info, err := os.Stat(cand)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return cand, nil
	}
	return "", exec.ErrNotFound
}

// ToolDirs lists directories to search in addition to PATH. CUDA installs
// outside PATH are the common case; versioned directories are listed
// newest first.
func ToolDirs() []string {
	var dirs []string
	for _, env := range []string{"CUDA_HOME", "CUDA_PATH"} {
		if v := os.Getenv(env); v != "" {
			dirs = append(dirs, filepath.Join(v, "bin"))
		}
	}
	dirs = append(dirs, "/usr/local/cuda/bin", "/opt/cuda/bin", "/usr/lib/cuda/bin")
	matches, _ := filepath.Glob("/usr/local/cuda-*/bin")
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return append(dirs, matches...)
}
