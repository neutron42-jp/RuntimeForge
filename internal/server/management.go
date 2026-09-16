package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"runtimeforge/internal/build"
	"runtimeforge/internal/config"
	"runtimeforge/internal/models"
	"runtimeforge/internal/runtimes"
	"runtimeforge/internal/selection"
	"runtimeforge/internal/source"
	"runtimeforge/internal/supervisor"
)

// --- sources ---------------------------------------------------------

func (s *Server) handleSourcesList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sources == nil {
		writeError(w, http.StatusServiceUnavailable, "source manager unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Sources.List())
}

func (s *Server) handleSourcesAdd(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sources == nil {
		writeError(w, http.StatusServiceUnavailable, "source manager unavailable")
		return
	}
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Ref  string `json:"ref"`
	}
	if err := ReadJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Name == "" {
		body.Name = deriveName(body.URL)
	}
	if err := s.deps.Sources.Add(source.Source{Name: body.Name, URL: body.URL, Ref: body.Ref}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	src, _ := s.deps.Sources.Get(body.Name)
	s.broadcast("sources", s.deps.Sources.List())
	writeJSON(w, http.StatusCreated, src)
}

func (s *Server) handleSourcesRemove(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sources == nil {
		writeError(w, http.StatusServiceUnavailable, "source manager unavailable")
		return
	}
	name := r.PathValue("name")
	purge := r.URL.Query().Get("purge") == "true"
	if err := s.deps.Sources.Remove(name, purge); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.broadcast("sources", s.deps.Sources.List())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSourcesCheck(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sources == nil {
		writeError(w, http.StatusServiceUnavailable, "source manager unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	src, err := s.deps.Sources.Check(ctx, r.PathValue("name"))
	if err != nil {
		src.LastError = err.Error()
	}
	writeJSON(w, http.StatusOK, src)
}

func (s *Server) handleSourcesBuild(w http.ResponseWriter, r *http.Request) {
	if s.deps.Builds == nil {
		writeError(w, http.StatusServiceUnavailable, "build engine unavailable")
		return
	}
	var body struct {
		Backend            string            `json:"backend"`
		Backends           []string          `json:"backends"`
		All                bool              `json:"all"`
		Commit             string            `json:"commit"`
		CMakeDefines       map[string]string `json:"cmake_defines"`
		ExtraConfigureArgs []string          `json:"extra_configure_args"`
		ExtraBuildArgs     []string          `json:"extra_build_args"`
		CC                 string            `json:"cc"`
		CXX                string            `json:"cxx"`
		Generator          string            `json:"generator"`
		BuildType          string            `json:"build_type"`
		ParallelJobs       int               `json:"parallel_jobs"`
	}
	if err := ReadJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	backends := append([]string(nil), body.Backends...)
	if body.Backend != "" {
		backends = append(backends, body.Backend)
	}
	if body.All {
		backends = nil
		for name, bc := range s.cfg().Build.Backends {
			if bc.Enabled {
				backends = append(backends, name)
			}
		}
	}
	if len(backends) == 0 {
		backends = []string{"cpu"}
	}

	req := build.Request{
		Source:             r.PathValue("name"),
		Commit:             body.Commit,
		Backends:           backends,
		CMakeDefines:       body.CMakeDefines,
		ExtraConfigureArgs: body.ExtraConfigureArgs,
		ExtraBuildArgs:     body.ExtraBuildArgs,
		CC:                 body.CC,
		CXX:                body.CXX,
		Generator:          body.Generator,
		BuildType:          body.BuildType,
		ParallelJobs:       body.ParallelJobs,
	}
	job, err := s.deps.Builds.Submit(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"jobs": []any{job}})
}

// --- builds ----------------------------------------------------------

func (s *Server) handleBuildsList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Builds == nil {
		writeError(w, http.StatusServiceUnavailable, "build engine unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Builds.Jobs())
}

func (s *Server) handleBuildGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.Builds == nil {
		writeError(w, http.StatusServiceUnavailable, "build engine unavailable")
		return
	}
	job, ok := s.deps.Builds.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "build not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleBuildCancel(w http.ResponseWriter, r *http.Request) {
	if s.deps.Builds == nil {
		writeError(w, http.StatusServiceUnavailable, "build engine unavailable")
		return
	}
	if !s.deps.Builds.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "build not running")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- runtimes --------------------------------------------------------

func (s *Server) handleRuntimesList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Runtimes == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime registry unavailable")
		return
	}
	list, err := s.deps.Runtimes.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []runtimes.Manifest{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRuntimeRemove(w http.ResponseWriter, r *http.Request) {
	if s.deps.Runtimes == nil {
		writeError(w, http.StatusServiceUnavailable, "runtime registry unavailable")
		return
	}
	if err := s.deps.Runtimes.Remove(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- selections ------------------------------------------------------

func (s *Server) handleSelectionsGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.Selections == nil {
		writeError(w, http.StatusServiceUnavailable, "selections unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Selections.Get())
}

func (s *Server) handleSelectionsPut(w http.ResponseWriter, r *http.Request) {
	if s.deps.Selections == nil {
		writeError(w, http.StatusServiceUnavailable, "selections unavailable")
		return
	}
	var body struct {
		Format    string `json:"format"`
		Model     string `json:"model"`
		RuntimeID string `json:"runtime_id"`
	}
	if err := ReadJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var err error
	if body.Model != "" {
		err = s.deps.Selections.SetModel(body.Model, body.RuntimeID)
	} else if body.Format != "" {
		err = s.deps.Selections.SetFormat(body.Format, body.RuntimeID)
	} else {
		writeError(w, http.StatusBadRequest, "either format or model is required")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Selections.Get())
}

// --- models ----------------------------------------------------------

func (s *Server) handleModelsList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Models == nil {
		writeError(w, http.StatusServiceUnavailable, "model registry unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Models.List())
}

func (s *Server) handleModelsScan(w http.ResponseWriter, r *http.Request) {
	if s.deps.Models == nil {
		writeError(w, http.StatusServiceUnavailable, "model registry unavailable")
		return
	}
	count, errs := s.scanModels()
	s.broadcast("models", s.deps.Models.List())
	writeJSON(w, http.StatusOK, map[string]any{"count": count, "errors": errs})
}

// handleModelSourcesGet returns the configured model scan sources.
func (s *Server) handleModelSourcesGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg().Models)
}

// handleModelSourcesPut replaces the configured scan sources, persists
// them, then rescans.
func (s *Server) handleModelSourcesPut(w http.ResponseWriter, r *http.Request) {
	if s.deps.Models == nil {
		writeError(w, http.StatusServiceUnavailable, "model registry unavailable")
		return
	}
	var body struct {
		ScanDirs  []string           `json:"scan_dirs"`
		ScanDepth *int               `json:"scan_depth"`
		Roots     []config.ModelRoot `json:"roots"`
		Files     []string           `json:"files"`
	}
	if err := ReadJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	modelsPatch := map[string]any{
		"scan_dirs": body.ScanDirs,
		"roots":     body.Roots,
		"files":     body.Files,
	}
	if body.ScanDepth != nil {
		modelsPatch["scan_depth"] = *body.ScanDepth
	}
	if s.deps.UpdateConfig != nil {
		if _, err := s.deps.UpdateConfig(map[string]any{"models": modelsPatch}); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	count, errs := s.scanModels()
	s.broadcast("models", s.deps.Models.List())
	writeJSON(w, http.StatusOK, map[string]any{
		"count":   count,
		"errors":  errs,
		"sources": s.cfg().Models,
	})
}

// scanModels scans the configured sources and replaces the registry.
func (s *Server) scanModels() (int, []string) {
	found, errs := s.scan()
	if err := s.deps.Models.Replace(found); err != nil {
		errs = append(errs, err.Error())
	}
	return len(found), errs
}

// autoScan rescans on startup but never empties the registry: if the
// configured sources are missing (unmounted drive, cleared config) the
// existing models are kept rather than silently wiped.
func (s *Server) autoScan() {
	found, _ := s.scan()
	if len(found) == 0 {
		return
	}
	_ = s.deps.Models.Replace(found)
	s.broadcast("models", s.deps.Models.List())
}

// scan walks the configured sources.
func (s *Server) scan() ([]models.Model, []string) {
	cfg := s.cfg()
	dirs, files := modelTargets(cfg)
	var errs []string
	for _, d := range dirs {
		if info, err := os.Stat(models.ExpandHome(d.Path)); err != nil || !info.IsDir() {
			errs = append(errs, d.Path+": directory not found")
		}
	}
	found, scanErrs := models.ScanTargets(dirs, files)
	for _, e := range scanErrs {
		errs = append(errs, e.Error())
	}
	return found, errs
}

// modelTargets converts configuration into scan targets.
func modelTargets(cfg config.Config) ([]models.Dir, []string) {
	var dirs []models.Dir
	for _, d := range cfg.Models.ScanDirs {
		dirs = append(dirs, models.Dir{Path: d, Depth: cfg.Models.ScanDepth})
	}
	for _, root := range cfg.Models.Roots {
		dirs = append(dirs, models.Dir{Path: root.Path, Depth: root.Depth})
	}
	return dirs, cfg.Models.Files
}

func (s *Server) handleModelLoad(w http.ResponseWriter, r *http.Request) {
	if s.deps.Models == nil || s.deps.Supervisor == nil || s.deps.Runtimes == nil {
		writeError(w, http.StatusServiceUnavailable, "load unavailable")
		return
	}
	model, ok := s.deps.Models.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}
	if model.Projector {
		writeError(w, http.StatusBadRequest, "multimodal projectors cannot be loaded directly")
		return
	}
	var body struct {
		RuntimeID   string   `json:"runtime_id"`
		ContextSize int      `json:"context_size"`
		GPULayers   string   `json:"gpu_layers"`
		Threads     int      `json:"threads"`
		ExtraArgs   []string `json:"extra_args"`
	}
	if err := ReadJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	manifest, err := s.resolveRuntime(model, body.RuntimeID)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	bin, err := s.deps.Runtimes.BinaryPath(manifest, "llama-server")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	cfg := s.cfg()
	params := supervisor.Params{
		ContextSize: cfg.Load.ContextSize,
		GPULayers:   cfg.Load.GPULayers,
		Threads:     cfg.Load.Threads,
		ExtraArgs:   append([]string(nil), cfg.Load.ExtraArgs...),
	}
	if body.ContextSize != 0 {
		params.ContextSize = body.ContextSize
	}
	if body.GPULayers != "" {
		params.GPULayers = body.GPULayers
	}
	if body.Threads != 0 {
		params.Threads = body.Threads
	}
	params.ExtraArgs = append(params.ExtraArgs, body.ExtraArgs...)

	inst, err := s.deps.Supervisor.Load(supervisor.LoadSpec{
		ModelID:      model.ID,
		ModelName:    model.Name,
		ModelPath:    model.Path,
		Architecture: model.Architecture,
		RuntimeID:    manifest.ID,
		Backend:      manifest.Backend,
		ServerBinary: bin,
		Params:       params,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, inst)
}

func (s *Server) handleModelUnload(w http.ResponseWriter, r *http.Request) {
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "supervisor unavailable")
		return
	}
	if err := s.deps.Supervisor.Unload(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "supervisor unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Supervisor.List())
}

// resolveRuntime picks the runtime for a model, honoring an explicit
// override and otherwise using the selection engine.
func (s *Server) resolveRuntime(model models.Model, overrideID string) (runtimes.Manifest, error) {
	list, err := s.deps.Runtimes.List()
	if err != nil {
		return runtimes.Manifest{}, err
	}
	if overrideID != "" {
		for _, m := range list {
			if m.ID == overrideID {
				return m, nil
			}
		}
		return runtimes.Manifest{}, &resolveError{"runtime " + overrideID + " is not installed"}
	}
	if s.deps.Selections == nil {
		return runtimes.Manifest{}, &resolveError{"no runtime specified and selections unavailable"}
	}
	res, err := selection.NewResolver(s.deps.Selections).Resolve(
		selection.ModelRef{ID: model.ID, Architecture: model.Architecture, Format: model.Format},
		list,
		selection.Options{
			BackendPriority: s.cfg().Runtime.BackendPriority,
			Available:       s.hw().Backends,
			AutoSelect:      s.cfg().Runtime.AutoSelect,
		},
	)
	if err != nil {
		return runtimes.Manifest{}, err
	}
	for _, m := range list {
		if m.ID == res.RuntimeID {
			return m, nil
		}
	}
	return runtimes.Manifest{}, &resolveError{"resolved runtime " + res.RuntimeID + " disappeared"}
}

type resolveError struct{ msg string }

func (e *resolveError) Error() string { return e.msg }

func deriveName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" {
		return ""
	}
	base := strings.TrimSuffix(u.Path, ".git")
	parts := strings.Split(strings.Trim(base, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	name := parts[len(parts)-1]
	if name == "" {
		name = "source"
	}
	return name
}
