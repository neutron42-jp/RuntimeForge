// Package source manages the Git repositories that provide llama.cpp
// runtime builds. Registry edits are local; network access happens only
// in Check (ls-remote) and clone/checkout (SPEC §7).
package source

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Git abstracts git execution for testing.
type Git interface {
	Run(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// ExecGit runs the system git binary.
type ExecGit struct{}

// Run implements Git.
func (ExecGit) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// ResolveRemote resolves ref on the remote to a commit hash using
// ls-remote. An empty ref resolves the remote HEAD. If ref is already a
// full commit hash it is returned unchanged.
func ResolveRemote(ctx context.Context, git Git, url, ref string) (string, error) {
	if isCommitHash(ref) {
		return ref, nil
	}
	patterns := refPatterns(ref)
	out, err := git.Run(ctx, "", append([]string{"ls-remote", "--", url}, patterns...)...)
	if err != nil {
		return "", err
	}
	return parseLsRemote(string(out), ref)
}

func refPatterns(ref string) []string {
	if ref == "" || ref == "HEAD" {
		return []string{"HEAD"}
	}
	return []string{
		"refs/heads/" + ref,
		"refs/tags/" + ref,
		"refs/tags/" + ref + "^{}",
		ref,
	}
}

// parseLsRemote picks the best matching commit for ref from ls-remote
// output, preferring branches, then tags, then HEAD.
func parseLsRemote(out, ref string) (string, error) {
	type entry struct{ sha, name string }
	var entries []entry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		entries = append(entries, entry{sha: fields[0], name: fields[1]})
	}
	if len(entries) == 0 {
		if ref == "" || ref == "HEAD" {
			return "", fmt.Errorf("remote HEAD not found")
		}
		return "", fmt.Errorf("ref %q not found on remote", ref)
	}
	prefer := func(prefix string) string {
		for _, e := range entries {
			if strings.HasPrefix(e.name, prefix) {
				return e.sha
			}
		}
		return ""
	}
	switch {
	case ref == "" || ref == "HEAD":
		if sha := prefer("HEAD"); sha != "" {
			return sha, nil
		}
	case ref != "":
		if sha := prefer("refs/heads/" + ref); sha != "" {
			return sha, nil
		}
		if sha := prefer("refs/tags/" + ref + "^{}"); sha != "" {
			return sha, nil
		}
		if sha := prefer("refs/tags/" + ref); sha != "" {
			return sha, nil
		}
		if sha := prefer(ref); sha != "" {
			return sha, nil
		}
	}
	return entries[0].sha, nil
}

func isCommitHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
