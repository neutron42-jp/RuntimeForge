package runtimes

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const sampleArch = `
static const std::map<llm_arch, const char *> LLM_ARCH_NAMES = {
    { LLM_ARCH_CLIP,   "clip" },
    { LLM_ARCH_LLAMA,  "llama" },
    { LLM_ARCH_QWEN3,  "qwen3" },
    { LLM_ARCH_GEMMA4, "gemma4" },
    { LLM_ARCH_KIMI_K3, "kimi-k3" },
    { LLM_ARCH_UNKNOWN, "(unknown)" },
};

static const std::map<llm_kv, const char *> LLM_KV_NAMES = {
    { LLM_KV_GENERAL_ARCHITECTURE, "general.architecture" },
    { LLM_KV_LLM_ARCH_NAME,        "general.architecture" },
};
`

func TestExtractArchitectures(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "llama-arch.cpp"), []byte(sampleArch), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ExtractArchitectures(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clip", "gemma4", "kimi-k3", "llama", "qwen3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("archs = %v, want %v", got, want)
	}
	// The KV map must not leak into the result.
	for _, a := range got {
		if a == "general.architecture" {
			t.Error("KV names leaked into architecture list")
		}
	}
}

func TestExtractArchitecturesMissing(t *testing.T) {
	if _, err := ExtractArchitectures(t.TempDir()); err == nil {
		t.Error("expected error for missing source")
	}
}

func TestExtractArchitecturesOnlyUnknown(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	body := `LLM_ARCH_NAMES = { { LLM_ARCH_UNKNOWN, "(unknown)" } };`
	os.WriteFile(filepath.Join(dir, "src", "llama-arch.cpp"), []byte(body), 0o644)
	if _, err := ExtractArchitectures(dir); err == nil {
		t.Error("expected error when no real architectures are listed")
	}
}
