package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepo creates a local git repository with two commits on branch
// "main" and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "second")
	return dir
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(trimNL(out))
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func newManager(t *testing.T) (*Manager, string) {
	t.Helper()
	base := t.TempDir()
	m, err := NewManager(filepath.Join(base, "sources"), filepath.Join(base, "state", "sources.json"), ExecGit{})
	if err != nil {
		t.Fatal(err)
	}
	return m, base
}

func TestEnsureDefault(t *testing.T) {
	m, _ := newManager(t)
	if err := m.EnsureDefault(); err != nil {
		t.Fatal(err)
	}
	s, ok := m.Get(UpstreamName)
	if !ok {
		t.Fatal("upstream not registered")
	}
	if s.URL != UpstreamURL {
		t.Errorf("url = %q", s.URL)
	}
	// idempotent
	if err := m.EnsureDefault(); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 1 {
		t.Errorf("expected a single source, got %d", len(m.List()))
	}
}

func TestAddRemoveList(t *testing.T) {
	m, _ := newManager(t)
	if err := m.Add(Source{Name: "fork-a", URL: "https://example.com/a.git", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(Source{Name: "fork-a", URL: "x"}); err == nil {
		t.Error("duplicate name should fail")
	}
	if err := m.Add(Source{Name: "bad name", URL: "x"}); err == nil {
		t.Error("invalid name should fail")
	}
	if err := m.Add(Source{Name: "no-url"}); err == nil {
		t.Error("missing url should fail")
	}
	if len(m.List()) != 1 {
		t.Fatalf("list = %v", m.List())
	}
	if err := m.Remove("fork-a", false); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 0 {
		t.Errorf("list after remove = %v", m.List())
	}
	if err := m.Remove("ghost", false); err == nil {
		t.Error("removing missing source should fail")
	}
}

func TestCheckAndCloneLocalRepo(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	head := gitHead(t, repo)

	m, _ := newManager(t)
	if err := m.Add(Source{Name: "local", URL: repo, Ref: "main"}); err != nil {
		t.Fatal(err)
	}

	// Check before clone: remote resolved, no local commit, update available.
	s, err := m.Check(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	if s.RemoteCommit != head {
		t.Errorf("remote = %q, want %q", s.RemoteCommit, head)
	}
	if !s.UpdateAvailable || s.LocalCommit != "" {
		t.Errorf("state = %+v", s)
	}

	// Clone and check again: up to date.
	if err := m.EnsureClone(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	s, err = m.Check(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	if s.UpdateAvailable {
		t.Errorf("should be up to date: %+v", s)
	}
	if s.LocalCommit != head {
		t.Errorf("local = %q, want %q", s.LocalCommit, head)
	}
}

func TestCheckDetectsNewCommit(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	m, _ := newManager(t)
	m.Add(Source{Name: "local", URL: repo, Ref: "main"})
	if err := m.EnsureClone(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Check(ctx, "local"); s.UpdateAvailable {
		t.Fatal("expected up to date")
	}

	// Add a commit to the upstream repo.
	if err := os.WriteFile(filepath.Join(repo, "three.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommit(t, repo)
	if s, _ := m.Check(ctx, "local"); !s.UpdateAvailable {
		t.Errorf("expected update after new upstream commit: %+v", s)
	}
}

func TestEnsureCheckoutCommit(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	m, _ := newManager(t)
	m.Add(Source{Name: "local", URL: repo, Ref: "main"})
	if err := m.EnsureClone(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	first := firstCommit(t, repo)
	head, err := m.EnsureCheckout(ctx, "local", first)
	if err != nil {
		t.Fatal(err)
	}
	if head != first {
		t.Errorf("head = %q, want %q", head, first)
	}
}

func TestResolveRemoteTagAndHash(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	runExec(t, repo, "tag", "v1.0.0")
	head := gitHead(t, repo)

	sha, err := ResolveRemote(ctx, ExecGit{}, repo, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if sha != head {
		t.Errorf("tag resolve = %q, want %q", sha, head)
	}

	sha, err = ResolveRemote(ctx, ExecGit{}, repo, head)
	if err != nil {
		t.Fatal(err)
	}
	if sha != head {
		t.Errorf("hash resolve = %q, want %q", sha, head)
	}
}

func TestResolveRemoteMissingRef(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	if _, err := ResolveRemote(ctx, ExecGit{}, repo, "does-not-exist"); err == nil {
		t.Error("expected error for missing ref")
	}
}

func runCommit(t *testing.T, dir string) {
	t.Helper()
	runExec(t, dir, "add", ".")
	runExec(t, dir, "commit", "-m", "more")
}

func runExec(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func firstCommit(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-list", "--max-parents=0", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(trimNL(out))
}
