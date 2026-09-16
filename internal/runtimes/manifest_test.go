package runtimes

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleManifest() Manifest {
	return Manifest{
		ID:                     MakeID("upstream", "0123456789abcdef0123", "cuda"),
		Source:                 "upstream",
		GitURL:                 "https://github.com/ggml-org/llama.cpp",
		Commit:                 "0123456789abcdef0123",
		Backend:                "cuda",
		BuiltAt:                time.Now().UTC(),
		Binaries:               map[string]string{"llama-server": "bin/llama-server"},
		SupportedArchitectures: []string{"gemma4", "llama", "qwen3"},
	}
}

func TestMakeIDAndShortCommit(t *testing.T) {
	m := sampleManifest()
	if m.ID != "upstream@0123456789ab/cuda" {
		t.Errorf("id = %q", m.ID)
	}
	if m.ShortCommit() != "0123456789ab" {
		t.Errorf("short = %q", m.ShortCommit())
	}
}

func TestRegistryWriteGetList(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(root)
	m := sampleManifest()
	if err := r.Write(m); err != nil {
		t.Fatal(err)
	}
	// Manifest must live at a predictable path.
	if _, err := os.Stat(r.ManifestPath(m.Source, m.Commit, m.Backend)); err != nil {
		t.Fatalf("manifest path: %v", err)
	}
	got, ok := r.Get(m.ID)
	if !ok {
		t.Fatal("runtime not found")
	}
	if got.Backend != "cuda" || !got.SupportsArchitecture("gemma4") {
		t.Errorf("got %+v", got)
	}

	// Create the binary so BinaryPath resolves.
	binDir := filepath.Join(root, m.Source, m.Commit, m.Backend, "bin")
	os.MkdirAll(binDir, 0o755)
	os.WriteFile(filepath.Join(binDir, "llama-server"), []byte("x"), 0o755)
	path, err := r.BinaryPath(got, "llama-server")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(binDir, "llama-server") {
		t.Errorf("binary path = %q", path)
	}

	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	if err := r.Remove(m.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = r.List()
	if len(list) != 0 {
		t.Errorf("list after remove = %v", list)
	}
}

func TestRegistryEmptyDir(t *testing.T) {
	r := NewRegistry(filepath.Join(t.TempDir(), "does-not-exist"))
	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if list != nil {
		t.Errorf("expected nil list, got %v", list)
	}
}

func TestFilterByArchitecture(t *testing.T) {
	a := Manifest{ID: "a", SupportedArchitectures: []string{"llama", "qwen3"}}
	b := Manifest{ID: "b", SupportedArchitectures: []string{"gemma4"}}
	got := FilterByArchitecture([]Manifest{a, b}, "qwen3")
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("filter = %v", got)
	}
}

func TestParseID(t *testing.T) {
	src, backend := ParseID("upstream@abc123/cuda")
	if src != "upstream" || backend != "cuda" {
		t.Errorf("parse = %q/%q", src, backend)
	}
}
