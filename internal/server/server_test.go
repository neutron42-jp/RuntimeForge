package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"runtimeforge/internal/appdir"
	"runtimeforge/internal/config"
	"runtimeforge/internal/gguf"
	"runtimeforge/internal/hardware"
	"runtimeforge/internal/models"
	"runtimeforge/internal/runtimes"
	"runtimeforge/internal/selection"
	"runtimeforge/internal/source"
	"runtimeforge/internal/supervisor"
)

func newTestServer(t *testing.T) (*Server, appdir.Paths, config.Config) {
	t.Helper()
	p := appdir.New(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	srcMgr, err := source.NewManager(p.Sources, p.StateFile("sources.json"), source.ExecGit{})
	if err != nil {
		t.Fatal(err)
	}
	srcMgr.EnsureDefault()
	modelReg, _ := models.NewRegistry(p.StateFile("models.json"))
	selStore, _ := selection.NewStore(p.StateFile("selections.json"))
	sup := supervisor.New(supervisor.Options{Paths: p, PortRange: []int{31000, 31010}})

	cur := cfg
	s, err := New(Deps{
		Paths:      p,
		Config:     func() config.Config { return cur },
		Hardware:   func() hardware.Report { return hardware.Report{} },
		Sources:    srcMgr,
		Runtimes:   runtimes.NewRegistry(p.Runtimes),
		Models:     modelReg,
		Selections: selStore,
		Supervisor: sup,
		UpdateConfig: func(partial map[string]any) (config.Config, error) {
			if err := config.SaveOverlay(p, partial); err != nil {
				return cur, err
			}
			next, err := config.Load(p)
			if err != nil {
				return cur, err
			}
			cur = next
			return next, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, p, cfg
}

func doJSON(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/api/v1/health", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("status = %v", body["status"])
	}
}

func TestConfigEndpoint(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/api/v1/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body config.Config
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Server.Port != 1234 {
		t.Errorf("port = %d", body.Server.Port)
	}
}

func TestConfigPutPersists(t *testing.T) {
	s, p, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPut, "/api/v1/config", map[string]any{
		"server":  map[string]any{"port": float64(4321)},
		"runtime": map[string]any{"backend_priority": []any{"vulkan", "cpu"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body config.Config
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Server.Port != 4321 {
		t.Errorf("port after put = %d", body.Server.Port)
	}
	if len(body.Runtime.BackendPriority) != 2 || body.Runtime.BackendPriority[0] != "vulkan" {
		t.Errorf("priority = %v", body.Runtime.BackendPriority)
	}

	// A second GET must reflect the persisted overlay.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/config", nil)
	var again config.Config
	json.Unmarshal(rec.Body.Bytes(), &again)
	if again.Server.Port != 4321 {
		t.Errorf("port not persisted: %d", again.Server.Port)
	}

	// The overlay must live in config.d, not config.toml.
	if _, err := os.Stat(filepath.Join(p.ConfigD, "90-user.toml")); err != nil {
		t.Errorf("overlay file missing: %v", err)
	}
	if _, err := os.Stat(p.Config); err == nil {
		t.Errorf("config.toml should not have been created/overwritten")
	}
}

func TestUIServesIndex(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "RuntimeForge") {
		t.Errorf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSPAFallbackButNotForAPI(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/models/42", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("spa fallback code = %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("api 404 code = %d", rec.Code)
	}
}

func TestSourcesLifecycle(t *testing.T) {
	s, _, _ := newTestServer(t)
	// Default upstream source exists.
	rec := doJSON(t, s, http.MethodGet, "/api/v1/sources", nil)
	var list []source.Source
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "upstream" {
		t.Fatalf("sources = %+v", list)
	}

	rec = doJSON(t, s, http.MethodPost, "/api/v1/sources", map[string]any{
		"name": "fork", "url": "https://example.com/fork.git", "ref": "main",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add status = %d body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sources/fork", nil)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNoContent {
		t.Errorf("delete status = %d", rec2.Code)
	}
}

func TestSelectionsEndpoint(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPut, "/api/v1/selections", map[string]any{
		"format": "gguf", "runtime_id": "upstream@abc/cpu",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/selections", nil)
	var sel selection.Selections
	json.Unmarshal(rec.Body.Bytes(), &sel)
	if sel.Formats["gguf"] != "upstream@abc/cpu" {
		t.Errorf("selections = %+v", sel)
	}
}

func TestOpenAIModelsListsSentinel(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/v1/models", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	found := false
	for _, m := range body.Data {
		if m["id"] == "loaded" {
			found = true
		}
	}
	if !found {
		t.Errorf("sentinel model missing: %+v", body.Data)
	}
}

func TestOpenAIProxyNoModelLoaded(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/v1/chat/completions", map[string]any{
		"model":    "loaded",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestModelsEmptyReturnsArray(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/api/v1/models", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("empty models should serialize as [], got %q", body)
	}
}

func TestAutoScanOnStartup(t *testing.T) {
	p := appdir.New(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	srcDir := t.TempDir()
	writeTestGGUF(t, filepath.Join(srcDir, "M-Q4_K_M.gguf"), "llama", "M", 15)
	if err := config.SaveOverlay(p, map[string]any{
		"models": map[string]any{
			"roots": []config.ModelRoot{{Path: srcDir, Depth: 0}},
			"files": []string{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	srcMgr, _ := source.NewManager(p.Sources, p.StateFile("sources.json"), source.ExecGit{})
	modelReg, _ := models.NewRegistry(p.StateFile("models.json"))
	selStore, _ := selection.NewStore(p.StateFile("selections.json"))
	sup := supervisor.New(supervisor.Options{Paths: p, PortRange: []int{31100, 31110}})

	s, err := New(Deps{
		Paths:      p,
		Config:     func() config.Config { return cfg },
		Hardware:   func() hardware.Report { return hardware.Report{} },
		Sources:    srcMgr,
		Runtimes:   runtimes.NewRegistry(p.Runtimes),
		Models:     modelReg,
		Selections: selStore,
		Supervisor: sup,
		AutoScan:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec := doJSON(t, s, http.MethodGet, "/api/v1/models", nil)
		if strings.Contains(rec.Body.String(), "M-Q4_K_M") || strings.Contains(rec.Body.String(), `"M"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("auto scan did not register the model")
}

func writeTestGGUF(t *testing.T, path, arch, name string, fileType uint32) {
	t.Helper()
	var b bytes.Buffer
	u32 := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) }
	u64 := func(v uint64) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { u64(uint64(len(s))); b.WriteString(s) }
	u32(gguf.Magic)
	u32(3)
	u64(0)
	u64(3)
	str(gguf.KeyArchitecture)
	u32(gguf.TypeString)
	str(arch)
	str(gguf.KeyName)
	u32(gguf.TypeString)
	str(name)
	str(gguf.KeyFileType)
	u32(gguf.TypeUint32)
	u32(fileType)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestModelSourcesRegisterAndScan(t *testing.T) {
	s, _, _ := newTestServer(t)

	root := t.TempDir()
	writeTestGGUF(t, filepath.Join(root, "top.gguf"), "llama", "Top", 15)
	writeTestGGUF(t, filepath.Join(root, "sub", "deep.gguf"), "gemma4", "Deep", 15)
	looseFile := filepath.Join(t.TempDir(), "Loose-Q8_0.gguf")
	writeTestGGUF(t, looseFile, "qwen3", "Loose", 7)

	// depth 0 => only the root file, plus the explicit loose file.
	rec := doJSON(t, s, http.MethodPut, "/api/v1/models/sources", map[string]any{
		"roots": []map[string]any{{"path": root, "depth": 0}},
		"files": []string{looseFile},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d body=%s", rec.Code, rec.Body.String())
	}
	var res struct {
		Count   int `json:"count"`
		Sources struct {
			Roots []struct {
				Path  string `json:"path"`
				Depth int    `json:"depth"`
			} `json:"roots"`
			Files []string `json:"files"`
		} `json:"sources"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Count != 2 {
		t.Errorf("count = %d, want 2 (root file + loose file at depth 0)", res.Count)
	}
	if len(res.Sources.Roots) != 1 || res.Sources.Roots[0].Depth != 0 {
		t.Errorf("roots not persisted: %+v", res.Sources.Roots)
	}

	rec = doJSON(t, s, http.MethodGet, "/api/v1/models", nil)
	var list []models.Model
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("models = %d, want 2", len(list))
	}

	rec = doJSON(t, s, http.MethodGet, "/api/v1/models/sources", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), root) {
		t.Errorf("sources get = %d %s", rec.Code, rec.Body.String())
	}
}

func TestScanReportsMissingDir(t *testing.T) {
	s, _, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPut, "/api/v1/models/sources", map[string]any{
		"roots": []map[string]any{{"path": filepath.Join(t.TempDir(), "nope"), "depth": -1}},
		"files": []string{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Errorf("expected a missing-directory warning: %s", rec.Body.String())
	}
}

func TestSelectInstance(t *testing.T) {
	now := time.Now()
	instances := []supervisor.Instance{
		{ModelID: "a", ModelName: "Model A", Status: supervisor.StateReady, StartedAt: now.Add(-time.Minute)},
		{ModelID: "b", ModelName: "Model B", Status: supervisor.StateReady, StartedAt: now},
		{ModelID: "c", ModelName: "Model C", Status: supervisor.StateLoading, StartedAt: now},
	}
	known := []models.Model{{ID: "d", Name: "Model D"}}

	// Sentinel resolves to the most recently started ready model.
	got, status, err := selectInstance(instances, known, "loaded")
	if err != nil || status != 0 {
		t.Fatalf("loaded: %v %d", err, status)
	}
	if got.ModelID != "b" {
		t.Errorf("sentinel chose %q, want b", got.ModelID)
	}

	// Explicit ready model by name.
	got, _, err = selectInstance(instances, known, "Model A")
	if err != nil || got.ModelID != "a" {
		t.Errorf("by name = %+v %v", got, err)
	}

	// Loading model is a conflict.
	if _, code, _ := selectInstance(instances, known, "c"); code != http.StatusConflict {
		t.Errorf("loading code = %d", code)
	}

	// Registered but not loaded is a conflict.
	if _, code, _ := selectInstance(instances, known, "Model D"); code != http.StatusConflict {
		t.Errorf("registered-not-loaded code = %d", code)
	}

	// Unknown is a 404.
	if _, code, _ := selectInstance(instances, known, "ghost"); code != http.StatusNotFound {
		t.Errorf("unknown code = %d", code)
	}
}
