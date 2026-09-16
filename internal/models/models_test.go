package models

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"runtimeforge/internal/gguf"
)

// writeGGUF writes a minimal GGUF file containing the metadata keys the
// scanner reads.
func writeGGUF(t *testing.T, path, arch, name string, fileType uint32) {
	t.Helper()
	var b bytes.Buffer
	u32 := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) }
	u64 := func(v uint64) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { u64(uint64(len(s))); b.WriteString(s) }
	kvString := func(k, v string) { str(k); u32(gguf.TypeString); str(v) }
	kvU32 := func(k string, v uint32) { str(k); u32(gguf.TypeUint32); u32(v) }

	u32(gguf.Magic)
	u32(3)
	u64(0) // tensors
	u64(3) // kv count
	kvString(gguf.KeyArchitecture, arch)
	kvString(gguf.KeyName, name)
	kvU32(gguf.KeyFileType, fileType)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanSingleFile(t *testing.T) {
	dir := t.TempDir()
	writeGGUF(t, filepath.Join(dir, "sub", "Qwen3-8B-Q4_K_M.gguf"), "qwen3", "Qwen3 8B", 15)

	list, errs := Scan([]string{dir})
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(list) != 1 {
		t.Fatalf("models = %d, want 1", len(list))
	}
	m := list[0]
	if m.Architecture != "qwen3" {
		t.Errorf("arch = %q", m.Architecture)
	}
	if m.Quantization != "Q4_K_M" {
		t.Errorf("quant = %q", m.Quantization)
	}
	if m.ShardTotal != 1 || len(m.Shards) != 1 {
		t.Errorf("shards = %d/%v", m.ShardTotal, m.Shards)
	}
	if m.Projector {
		t.Error("should not be a projector")
	}
	if m.SizeBytes <= 0 {
		t.Errorf("size = %d", m.SizeBytes)
	}
}

func TestScanSplitShards(t *testing.T) {
	dir := t.TempDir()
	// Only the first shard carries metadata.
	writeGGUF(t, filepath.Join(dir, "Big-00001-of-00003.gguf"), "llama", "Big", 15)
	// Other shards are empty placeholders.
	for _, name := range []string{"Big-00002-of-00003.gguf", "Big-00003-of-00003.gguf"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	list, errs := Scan([]string{dir})
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(list) != 1 {
		t.Fatalf("models = %d, want 1 (shards must be grouped)", len(list))
	}
	m := list[0]
	if m.ShardTotal != 3 {
		t.Errorf("shard total = %d", m.ShardTotal)
	}
	if len(m.Shards) != 3 {
		t.Errorf("shards = %v", m.Shards)
	}
	if filepath.Base(m.Path) != "Big-00001-of-00003.gguf" {
		t.Errorf("primary = %q", m.Path)
	}
}

func TestScanProjectorFlag(t *testing.T) {
	dir := t.TempDir()
	writeGGUF(t, filepath.Join(dir, "mmproj-F32.gguf"), "clip", "mmproj", 0)
	list, _ := Scan([]string{dir})
	if len(list) != 1 || !list[0].Projector {
		t.Errorf("projector not flagged: %+v", list)
	}
}

func TestScanMissingDirIsIgnored(t *testing.T) {
	list, errs := Scan([]string{filepath.Join(t.TempDir(), "nope")})
	if len(list) != 0 || len(errs) != 0 {
		t.Errorf("list=%v errs=%v", list, errs)
	}
}

func TestRegistryPersistence(t *testing.T) {
	dir := t.TempDir()
	writeGGUF(t, filepath.Join(dir, "m-Q8_0.gguf"), "llama", "M", 7)
	list, _ := Scan([]string{dir})

	path := filepath.Join(t.TempDir(), "models.json")
	r, err := NewRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Replace(list); err != nil {
		t.Fatal(err)
	}

	r2, err := NewRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	got := r2.List()
	if len(got) != 1 || got[0].Quantization != "Q8_0" {
		t.Errorf("persisted = %+v", got)
	}
	if _, ok := r2.Get(got[0].ID); !ok {
		t.Error("Get by id failed")
	}
}

func TestDisplayName(t *testing.T) {
	cases := map[string]string{
		"/m/K2-Horizon-7B-GGUF/K2-Horizon-7B-Q4_K_S.gguf":                                           "K2-Horizon-7B",
		"/m/LiquidAI/LFM2.5-1.2B-Instruct-GGUF/LFM2.5-1.2B-Instruct-Q8_0.gguf":                      "LFM2.5-1.2B-Instruct",
		"/m/unsloth/Qwen3.8-Flash-Next-UD-IQ4_XS/Qwen3.8-Flash-Next-UD-IQ4_XS-00001-of-00003.gguf":  "Qwen3.8-Flash-Next-UD",
		"/m/gemma-4-26B-A4B-it-qat-GGUF/mmproj-F32.gguf":                                            "gemma-4-26B-A4B-it-qat",
		"/m/Huihui-Ornith-1.5-9B-abliterated-GGUF/Huihui-Ornith-1.5-9B-abliterated.mmproj-f16.gguf": "Huihui-Ornith-1.5-9B-abliterated",
		"/m/lmstudio-community/Bonsai-27B-GGUF/Bonsai-27B-Q1_0.gguf":                                "Bonsai-27B",
		"/m/NVIDIA-Nemotron-3-Nano-4B-GGUF/NVIDIA-Nemotron-3-Nano-4B-Q4_K_M.gguf":                   "NVIDIA-Nemotron-3-Nano-4B",
		"/m/mmnga/llm-jp-3.1-1.8b-instruct4-gguf/llm-jp-3.1-1.8b-instruct4-Q4_0.gguf":               "llm-jp-3.1-1.8b-instruct4",
		"/m/Qwen3.8-27B-GGUF/Qwen3.8-27B-UD-IQ3_XXS.gguf":                                           "Qwen3.8-27B-UD",
	}
	for path, want := range cases {
		got := displayName(path)
		if got != want {
			t.Errorf("displayName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestScanUsesFileNameNotMetadataName(t *testing.T) {
	dir := t.TempDir()
	// Metadata name is a hash (as LiquidAI publishes), file name is real.
	path := filepath.Join(dir, "LFM2.5-1.2B-Instruct-GGUF", "LFM2.5-1.2B-Instruct-Q8_0.gguf")
	writeGGUF(t, path, "lfm2", "4cd563d5a96af9e7c738b76cd89a0a200db7608f", 7)
	list, _ := Scan([]string{dir})
	if len(list) != 1 {
		t.Fatal("expected one model")
	}
	if list[0].Name != "LFM2.5-1.2B-Instruct" {
		t.Errorf("name = %q, want file-derived name", list[0].Name)
	}
	if list[0].MetaName != "4cd563d5a96af9e7c738b76cd89a0a200db7608f" {
		t.Errorf("meta name = %q", list[0].MetaName)
	}
}

func TestRegistryMigrationRecomputesName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "K2-Horizon-7B-GGUF", "K2-Horizon-7B-Q4_K_S.gguf")
	writeGGUF(t, p, "k2-horizon", "Checkpoint_0002500", 15)

	// Simulate a registry written by an older version with a stale name.
	regPath := filepath.Join(t.TempDir(), "models.json")
	stale, err := json.Marshal(map[string]any{
		"models": []Model{{ID: "x", Name: "Checkpoint_0002500", Path: p}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regPath, stale, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := NewRegistry(regPath)
	if err != nil {
		t.Fatal(err)
	}
	list := r.List()
	if len(list) != 1 || list[0].Name != "K2-Horizon-7B" {
		t.Errorf("name not migrated: %+v", list)
	}
}

func TestScanEmptyDir(t *testing.T) {
	list, errs := Scan([]string{t.TempDir()})
	if len(list) != 0 || len(errs) != 0 {
		t.Errorf("list=%v errs=%v", list, errs)
	}
}

func TestScanTargetsDepth(t *testing.T) {
	root := t.TempDir()
	writeGGUF(t, filepath.Join(root, "top.gguf"), "llama", "Top", 15)
	writeGGUF(t, filepath.Join(root, "a", "one.gguf"), "llama", "One", 15)
	writeGGUF(t, filepath.Join(root, "a", "b", "two.gguf"), "llama", "Two", 15)

	counts := func(depth int) int {
		list, errs := ScanTargets([]Dir{{Path: root, Depth: depth}}, nil)
		if len(errs) != 0 {
			t.Fatalf("depth %d errs: %v", depth, errs)
		}
		return len(list)
	}
	if got := counts(0); got != 1 {
		t.Errorf("depth 0 found %d models, want 1 (root only)", got)
	}
	if got := counts(1); got != 2 {
		t.Errorf("depth 1 found %d models, want 2", got)
	}
	if got := counts(2); got != 3 {
		t.Errorf("depth 2 found %d models, want 3", got)
	}
	if got := counts(-1); got != 3 {
		t.Errorf("depth -1 found %d models, want 3 (unlimited)", got)
	}
}

func TestScanTargetsExplicitFiles(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a", "ModelA-Q4_K_M.gguf")
	fileB := filepath.Join(dir, "b", "ModelB-Q8_0.gguf")
	writeGGUF(t, fileA, "llama", "A", 15)
	writeGGUF(t, fileB, "gemma4", "B", 7)
	// A non-existent file is reported but does not abort.
	list, errs := ScanTargets(nil, []string{fileA, fileB, filepath.Join(dir, "missing.gguf")})
	if len(list) != 2 {
		t.Fatalf("found %d models, want 2", len(list))
	}
	if len(errs) != 1 {
		t.Errorf("errs = %v, want 1 for missing file", errs)
	}
	if list[0].SourceDir == "" {
		t.Errorf("source dir not set: %+v", list[0])
	}
}
