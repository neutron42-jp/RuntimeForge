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
	p := supervisor.Params{
		ContextSize:  8192,
		KVCacheTypeK: "q8_0",
		KVCacheTypeV: "q8_0",
		GPULayers:    "99",
		Threads:      8,
		CPURange:     "0-7",
		ExtraArgs:    []string{"--flash-attn"},
	}
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
	if got.ContextSize != 8192 || got.KVCacheTypeK != "q8_0" || got.Threads != 8 || got.GPULayers != "99" {
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
	base := supervisor.Params{
		ContextSize:  4096,
		KVCacheTypeK: "f16",
		GPULayers:    "0",
		Threads:      4,
		ExtraArgs:    []string{"--global"},
	}
	over := supervisor.Params{
		ContextSize:  16384,
		KVCacheTypeV: "q4_0",
		Threads:      12,
		ExtraArgs:    []string{"--per-model"},
	}
	got := Merge(base, over)
	if got.ContextSize != 16384 {
		t.Errorf("context = %d, want 16384", got.ContextSize)
	}
	if got.KVCacheTypeK != "f16" {
		t.Errorf("cache K should keep base value, got %q", got.KVCacheTypeK)
	}
	if got.KVCacheTypeV != "q4_0" {
		t.Errorf("cache V = %q", got.KVCacheTypeV)
	}
	if got.GPULayers != "0" {
		t.Errorf("gpu layers = %q, want base 0", got.GPULayers)
	}
	if got.Threads != 12 {
		t.Errorf("threads = %d", got.Threads)
	}
	if len(got.ExtraArgs) != 2 || got.ExtraArgs[0] != "--global" || got.ExtraArgs[1] != "--per-model" {
		t.Errorf("extra args = %v", got.ExtraArgs)
	}
}
