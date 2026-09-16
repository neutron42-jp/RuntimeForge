package hardware

import (
	"context"
	"errors"
	"os/exec"
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

// LookPath implements CommandRunner.
func (ExecRunner) LookPath(file string) (string, error) { return exec.LookPath(file) }

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
