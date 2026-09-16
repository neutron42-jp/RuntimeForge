package appdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHomeRespectsEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	got, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("Home() = %q, want %q", got, dir)
	}
}

func TestHomeDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, "")
	t.Setenv("HOME", home)
	got, err := Home()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".runtimeforge")
	if got != want {
		t.Fatalf("Home() = %q, want %q", got, want)
	}
}

func TestExpand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := map[string]string{
		"~":         home,
		"~/models":  filepath.Join(home, "models"),
		"/abs/path": "/abs/path",
		"rel/path":  "rel/path",
	}
	for in, want := range cases {
		got, err := Expand(in)
		if err != nil {
			t.Fatalf("Expand(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnsureCreatesLayout(t *testing.T) {
	p := New(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{p.Root, p.ConfigD, p.Sources, p.Runtimes, p.Models, p.State, p.Logs, p.Cache} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("stat %s: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
	if got := p.StateFile("models.json"); got != filepath.Join(p.State, "models.json") {
		t.Errorf("StateFile = %q", got)
	}
}
