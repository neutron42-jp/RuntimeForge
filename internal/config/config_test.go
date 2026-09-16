package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"runtimeforge/internal/appdir"
)

func TestDefaultsDecode(t *testing.T) {
	var c Config
	if err := Decode(Defaults(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Server.Port != 1234 {
		t.Errorf("port = %d, want 1234", c.Server.Port)
	}
	if c.Build.Generator != "Ninja" {
		t.Errorf("generator = %q", c.Build.Generator)
	}
	if len(c.Runtime.BackendPriority) != 3 || c.Runtime.BackendPriority[0] != "cuda" {
		t.Errorf("backend_priority = %v", c.Runtime.BackendPriority)
	}
	for _, name := range []string{"cuda", "vulkan", "cpu"} {
		if _, ok := c.Build.Backends[name]; !ok {
			t.Errorf("missing backend %q", name)
		}
	}
	if c.Build.CMakeDefines["GGML_NATIVE"] != "ON" {
		t.Errorf("cmake_defines = %v", c.Build.CMakeDefines)
	}
	if c.Build.CC != "gcc-15" || c.Build.CXX != "g++-15" {
		t.Errorf("compiler defaults = %q/%q, want gcc-15/g++-15", c.Build.CC, c.Build.CXX)
	}
	if c.Build.Backends["cuda"].CC != "" {
		t.Errorf("cuda cc = %q, want empty (inherit)", c.Build.Backends["cuda"].CC)
	}
}

func TestMergeScalarReplace(t *testing.T) {
	base := map[string]any{"server": map[string]any{"port": int64(1234), "host": "127.0.0.1"}}
	over := map[string]any{"server": map[string]any{"port": int64(9000)}}
	out := Merge(base, over)
	srv := out["server"].(map[string]any)
	if srv["port"] != int64(9000) {
		t.Errorf("port = %v, want 9000", srv["port"])
	}
	if srv["host"] != "127.0.0.1" {
		t.Errorf("host lost: %v", srv["host"])
	}
	// base must not be mutated
	if base["server"].(map[string]any)["port"] != int64(1234) {
		t.Error("Merge mutated base")
	}
}

func TestMergeListReplaceAndAppend(t *testing.T) {
	base := map[string]any{"runtime": map[string]any{"backend_priority": []any{"cuda", "vulkan", "cpu"}}}
	replace := Merge(base, map[string]any{"runtime": map[string]any{"backend_priority": []any{"cpu"}}})
	if got := replace["runtime"].(map[string]any)["backend_priority"]; !reflect.DeepEqual(got, []any{"cpu"}) {
		t.Errorf("replace = %v", got)
	}
	appendOut := Merge(base, map[string]any{"runtime": map[string]any{"+backend_priority": []any{"rocm"}}})
	got := appendOut["runtime"].(map[string]any)["backend_priority"]
	if !reflect.DeepEqual(got, []any{"cuda", "vulkan", "cpu", "rocm"}) {
		t.Errorf("append = %v", got)
	}
}

func TestLayeredOrder(t *testing.T) {
	root := t.TempDir()
	p := appdir.New(root)
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(p.Config, "[server]\nport = 1500\n")
	write(filepath.Join(p.ConfigD, "10-a.toml"), "[server]\nport = 1600\n")
	write(filepath.Join(p.ConfigD, "20-b.toml"), "[server]\nhost = \"0.0.0.0\"\n")

	overlay := map[string]any{"server": map[string]any{"port": int64(1700)}}
	c, err := Load(p, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Port != 1700 {
		t.Errorf("port = %d, want 1700 (overlay wins)", c.Server.Port)
	}
	if c.Server.Host != "0.0.0.0" {
		t.Errorf("host = %q, want 0.0.0.0 (from config.d)", c.Server.Host)
	}
	// app models dir always present
	found := false
	for _, d := range c.Models.ScanDirs {
		if d == p.Models {
			found = true
		}
	}
	if !found {
		t.Errorf("scan_dirs = %v, want to include %q", c.Models.ScanDirs, p.Models)
	}
}

func TestEnsureDefaultFile(t *testing.T) {
	p := appdir.New(t.TempDir())
	p.Ensure()
	created, err := EnsureDefaultFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected file to be created")
	}
	created, err = EnsureDefaultFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected file to be kept")
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Port != 1234 {
		t.Errorf("port = %d", c.Server.Port)
	}
}

func TestSaveOverlayModelRootRoundTrip(t *testing.T) {
	p := appdir.New(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	partial := map[string]any{
		"models": map[string]any{
			"roots": []ModelRoot{{Path: "/data/models", Depth: 3}},
			"files": []string{"/data/a.gguf"},
		},
	}
	if err := SaveOverlay(p, partial); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.ConfigD, "90-user.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Keys must be lowercase TOML so they decode back into the struct.
	if !strings.Contains(string(data), `path = "/data/models"`) || !strings.Contains(string(data), "depth = 3") {
		t.Errorf("overlay encoded with wrong keys:\n%s", data)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models.Roots) != 1 || c.Models.Roots[0].Path != "/data/models" || c.Models.Roots[0].Depth != 3 {
		t.Errorf("roots did not round-trip: %+v", c.Models.Roots)
	}
	if len(c.Models.Files) != 1 || c.Models.Files[0] != "/data/a.gguf" {
		t.Errorf("files did not round-trip: %+v", c.Models.Files)
	}
}

func TestConfigDirPaths(t *testing.T) {
	p := appdir.New("/tmp/x")
	if p.ConfigD != "/tmp/x/config.d" {
		t.Errorf("ConfigD = %q", p.ConfigD)
	}
}

func TestSaveOverlayMergesAndLoads(t *testing.T) {
	p := appdir.New(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	// JSON numbers arrive as float64; they must round-trip as integers.
	if err := SaveOverlay(p, map[string]any{"server": map[string]any{"port": float64(4321)}}); err != nil {
		t.Fatal(err)
	}
	// A second overlay must merge, not replace.
	if err := SaveOverlay(p, map[string]any{"runtime": map[string]any{"backend_priority": []any{"vulkan", "cpu"}}}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Port != 4321 {
		t.Errorf("port = %d, want 4321", c.Server.Port)
	}
	if len(c.Runtime.BackendPriority) != 2 || c.Runtime.BackendPriority[0] != "vulkan" {
		t.Errorf("priority = %v", c.Runtime.BackendPriority)
	}
	// Overlay lives in config.d, not config.toml.
	if _, err := os.Stat(filepath.Join(p.ConfigD, "90-user.toml")); err != nil {
		t.Errorf("overlay file missing: %v", err)
	}
	if _, err := os.Stat(p.Config); err == nil {
		t.Errorf("config.toml should not exist")
	}
}
