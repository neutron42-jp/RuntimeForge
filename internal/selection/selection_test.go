package selection

import (
	"path/filepath"
	"testing"
	"time"

	"runtimeforge/internal/runtimes"
)

func manifest(id, backend string, builtAgo time.Duration, archs ...string) runtimes.Manifest {
	return runtimes.Manifest{
		ID:                     id,
		Backend:                backend,
		BuiltAt:                time.Now().Add(-builtAgo),
		SupportedArchitectures: archs,
	}
}

func testList() []runtimes.Manifest {
	return []runtimes.Manifest{
		manifest("upstream@aaa/cpu", "cpu", 1*time.Hour, "llama", "qwen4exp"),
		manifest("upstream@aaa/cuda", "cuda", 2*time.Hour, "llama", "qwen4exp"),
		manifest("upstream@aaa/vulkan", "vulkan", 30*time.Minute, "llama"),
		manifest("fork@bbb/cuda", "cuda", 10*time.Minute, "kimi-k3"),
	}
}

func TestAutoSelectCombinedRuntime(t *testing.T) {
	combined := runtimes.Manifest{
		ID:                     "upstream@x/cpu+cuda+vulkan",
		Backend:                "cpu+cuda+vulkan",
		Backends:               []string{"cpu", "cuda", "vulkan"},
		BuiltAt:                time.Now(),
		SupportedArchitectures: []string{"llama"},
	}
	cpuOnly := runtimes.Manifest{
		ID:                     "upstream@x/cpu",
		Backend:                "cpu",
		Backends:               []string{"cpu"},
		BuiltAt:                time.Now(),
		SupportedArchitectures: []string{"llama"},
	}
	r := NewResolver(nil)
	// A combined runtime serves the best available backend, so it wins
	// even against a newer CPU-only build.
	res, err := r.Resolve(ModelRef{ID: "m", Architecture: "llama"}, []runtimes.Manifest{cpuOnly, combined}, Options{
		BackendPriority: []string{"cuda", "vulkan", "cpu"},
		Available:       []string{"cuda", "vulkan", "cpu"},
		AutoSelect:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RuntimeID != combined.ID {
		t.Errorf("chose %q, want combined runtime", res.RuntimeID)
	}

	// With no GPU available, both fall back to cpu; the CPU-only build is
	// then preferred (same rank) only if newer.
	res, err = r.Resolve(ModelRef{ID: "m", Architecture: "llama"}, []runtimes.Manifest{combined}, Options{
		BackendPriority: []string{"cuda", "vulkan", "cpu"},
		Available:       []string{"cpu"},
		AutoSelect:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RuntimeID != combined.ID {
		t.Errorf("combined runtime should still be usable on cpu: %q", res.RuntimeID)
	}

	// A GPU-only runtime is excluded when no GPU backend is available.
	gpuOnly := runtimes.Manifest{
		ID: "upstream@x/cuda", Backend: "cuda", Backends: []string{"cuda"},
		BuiltAt: time.Now(), SupportedArchitectures: []string{"llama"},
	}
	if _, err := r.Resolve(ModelRef{ID: "m", Architecture: "llama"}, []runtimes.Manifest{gpuOnly}, Options{
		BackendPriority: []string{"cuda", "vulkan", "cpu"},
		Available:       []string{"cpu"},
		AutoSelect:      true,
	}); err == nil {
		t.Error("expected error when only a GPU runtime exists but no GPU is available")
	}
}

func TestAutoSelectPrefersBackendPriorityThenNewest(t *testing.T) {
	r := NewResolver(nil)
	res, err := r.Resolve(ModelRef{ID: "m1", Architecture: "qwen4exp"}, testList(), Options{
		BackendPriority: []string{"cuda", "vulkan", "cpu"},
		Available:       []string{"cuda", "vulkan", "cpu"},
		AutoSelect:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RuntimeID != "upstream@aaa/cuda" {
		t.Errorf("chose %q, want cuda runtime", res.RuntimeID)
	}
	if !res.Auto {
		t.Error("expected auto")
	}
}

func TestAutoSelectRespectsAvailableBackends(t *testing.T) {
	r := NewResolver(nil)
	// No CUDA on host: must fall back to vulkan/cpu.
	res, err := r.Resolve(ModelRef{ID: "m1", Architecture: "llama"}, testList(), Options{
		BackendPriority: []string{"cuda", "vulkan", "cpu"},
		Available:       []string{"vulkan", "cpu"},
		AutoSelect:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend != "vulkan" && res.Backend != "cpu" {
		t.Errorf("chose backend %q despite cuda unavailable", res.Backend)
	}
}

func TestAutoSelectNewestWithinBackend(t *testing.T) {
	list := []runtimes.Manifest{
		manifest("a/cuda", "cuda", 3*time.Hour, "llama"),
		manifest("b/cuda", "cuda", 1*time.Minute, "llama"),
	}
	r := NewResolver(nil)
	res, _ := r.Resolve(ModelRef{ID: "m", Architecture: "llama"}, list, Options{
		BackendPriority: []string{"cuda", "cpu"}, Available: []string{"cuda"}, AutoSelect: true,
	})
	if res.RuntimeID != "b/cuda" {
		t.Errorf("chose %q, want newest b/cuda", res.RuntimeID)
	}
}

func TestAutoSelectNoArchMatch(t *testing.T) {
	r := NewResolver(nil)
	_, err := r.Resolve(ModelRef{ID: "m", Architecture: "brand-new"}, testList(), Options{
		BackendPriority: []string{"cuda"}, Available: []string{"cuda"}, AutoSelect: true,
	})
	if err == nil {
		t.Fatal("expected error when no runtime supports the architecture")
	}
}

func TestAutoSelectUnknownArch(t *testing.T) {
	r := NewResolver(nil)
	if _, err := r.Resolve(ModelRef{ID: "m"}, testList(), Options{AutoSelect: true}); err == nil {
		t.Fatal("expected error for unknown architecture")
	}
}

func TestAutoDisabled(t *testing.T) {
	r := NewResolver(nil)
	if _, err := r.Resolve(ModelRef{ID: "m", Architecture: "llama"}, testList(), Options{AutoSelect: false}); err == nil {
		t.Fatal("expected error when auto select is off")
	}
}

func TestModelOverrideWins(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sel.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetModel("m1", "fork@bbb/cuda"); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(store)
	res, err := r.Resolve(ModelRef{ID: "m1", Architecture: "qwen4exp"}, testList(), Options{
		BackendPriority: []string{"cuda"}, Available: []string{"cuda"}, AutoSelect: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RuntimeID != "fork@bbb/cuda" || res.Auto {
		t.Errorf("model override ignored: %+v", res)
	}
	if res.Reason != "model override" {
		t.Errorf("reason = %q", res.Reason)
	}
}

func TestFormatOverrideUsedWhenNoModelOverride(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sel.json"))
	store.SetFormat("gguf", "upstream@aaa/vulkan")
	store.SetModel("other", "fork@bbb/cuda")
	r := NewResolver(store)
	res, err := r.Resolve(ModelRef{ID: "m2", Format: "gguf", Architecture: "llama"}, testList(), Options{AutoSelect: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.RuntimeID != "upstream@aaa/vulkan" || res.Auto {
		t.Errorf("format override ignored: %+v", res)
	}
}

func TestOverrideMissingRuntime(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sel.json"))
	store.SetModel("m1", "ghost@zzz/cuda")
	r := NewResolver(store)
	if _, err := r.Resolve(ModelRef{ID: "m1", Architecture: "llama"}, testList(), Options{AutoSelect: true}); err == nil {
		t.Fatal("expected error for missing overridden runtime")
	}
}

func TestStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sel.json")
	s1, _ := NewStore(path)
	s1.SetModel("m", "r")
	s1.SetFormat("gguf", "r2")

	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Get()
	if got.Models["m"] != "r" || got.Formats["gguf"] != "r2" {
		t.Errorf("persisted = %+v", got)
	}
	// Clearing removes the entry.
	s2.SetModel("m", "")
	if _, ok := s2.Get().Models["m"]; ok {
		t.Error("model override not cleared")
	}
}
