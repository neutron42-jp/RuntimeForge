package loadsettings

import (
	"path/filepath"
	"testing"

	"runtimeforge/internal/supervisor"
)

func TestStorePersistAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model_settings.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := supervisor.Params{Args: supervisor.Args{
		{Name: "-c", Value: "8192"},
		{Name: "--flash-attn", Value: "on"},
		{Name: "-ngl", Value: "99"},
	}}
	if err := s.Set("model-1", p); err != nil {
		t.Fatal(err)
	}
	if !s.Has("model-1") {
		t.Fatal("Has returned false")
	}

	// Reload from disk.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Get("model-1")
	if len(got.Args) != 3 || got.Args[0] != (supervisor.Arg{Name: "-c", Value: "8192"}) || got.Args[2].Name != "-ngl" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	if err := s2.Delete("model-1"); err != nil {
		t.Fatal(err)
	}
	if s2.Has("model-1") {
		t.Error("entry not deleted")
	}
	s3, _ := NewStore(path)
	if s3.Has("model-1") {
		t.Error("delete not persisted")
	}
}

func TestMerge(t *testing.T) {
	base := supervisor.Params{Args: supervisor.Args{
		{Name: "-c", Value: "4096"},
		{Name: "--threads", Value: "4"},
	}}
	over := supervisor.Params{Args: supervisor.Args{
		{Name: "--no-webui"},
	}}
	got := Merge(base, over)
	if len(got.Args) != 3 {
		t.Fatalf("args = %v", got.Args)
	}
	if got.Args[0].Name != "-c" || got.Args[1].Name != "--threads" || got.Args[2].Name != "--no-webui" {
		t.Errorf("merge order wrong: %v", got.Args)
	}
	// base must not be mutated.
	if len(base.Args) != 2 {
		t.Errorf("base mutated: %v", base.Args)
	}
}
