// Package server hosts the management API, the OpenAI-compatible API
// and the embedded web UI (SPEC §3, §14, §15).
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"runtimeforge/internal/appdir"
	"runtimeforge/internal/build"
	"runtimeforge/internal/config"
	"runtimeforge/internal/hardware"
	"runtimeforge/internal/loadsettings"
	"runtimeforge/internal/models"
	"runtimeforge/internal/runtimes"
	"runtimeforge/internal/selection"
	"runtimeforge/internal/source"
	"runtimeforge/internal/supervisor"
	"runtimeforge/internal/version"
	"runtimeforge/web"
)

// Deps are the runtime dependencies of the HTTP server. Optional
// fields may be nil in tests.
type Deps struct {
	Paths      appdir.Paths
	Config     func() config.Config
	Hardware   func() hardware.Report
	Sources    *source.Manager
	Builds     *build.Engine
	Runtimes   *runtimes.Registry
	Models     *models.Registry
	Selections *selection.Store
	Supervisor *supervisor.Supervisor
	// Settings holds per-model llama-server parameters.
	Settings *loadsettings.Store
	// UpdateConfig merges a partial configuration and returns the new
	// effective configuration.
	UpdateConfig func(partial map[string]any) (config.Config, error)
	// AutoScan rescans configured model sources in the background on
	// startup so the registry reflects files added since last run.
	AutoScan bool
}

// Server is the RuntimeForge HTTP server.
type Server struct {
	deps Deps
	mux  *http.ServeMux

	mu    sync.Mutex
	subs  map[int]chan []byte
	subID int
}

// New constructs a Server with all routes registered.
func New(deps Deps) (*Server, error) {
	s := &Server{deps: deps, mux: http.NewServeMux(), subs: map[int]chan []byte{}}
	ui, err := web.FS()
	if err != nil {
		return nil, fmt.Errorf("load embedded ui: %w", err)
	}
	if deps.Supervisor != nil {
		deps.Supervisor.SetOnChange(func(inst supervisor.Instance) {
			s.broadcast("instance", inst)
		})
		deps.Supervisor.SetOnLog(func(id, line string) {
			s.broadcast("server-log", map[string]string{"instance_id": id, "line": line})
		})
	}
	if deps.Builds != nil {
		go s.forwardBuildEvents(deps.Builds)
	}
	if deps.AutoScan && deps.Models != nil {
		go s.autoScan()
	}
	s.routes(ui)
	return s, nil
}

// Handler exposes the root handler (useful in tests).
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) cfg() config.Config {
	if s.deps.Config == nil {
		return config.Config{}
	}
	return s.deps.Config()
}

func (s *Server) hw() hardware.Report {
	if s.deps.Hardware == nil {
		return hardware.Report{}
	}
	return s.deps.Hardware()
}

func (s *Server) routes(ui fs.FS) {
	m := s.mux

	m.HandleFunc("GET /api/v1/health", s.handleHealth)
	m.HandleFunc("GET /api/v1/system", s.handleSystem)
	m.HandleFunc("GET /api/v1/config", s.handleConfig)
	m.HandleFunc("PUT /api/v1/config", s.handleConfigPut)

	// Sources
	m.HandleFunc("GET /api/v1/sources", s.handleSourcesList)
	m.HandleFunc("POST /api/v1/sources", s.handleSourcesAdd)
	m.HandleFunc("DELETE /api/v1/sources/{name}", s.handleSourcesRemove)
	m.HandleFunc("POST /api/v1/sources/{name}/check", s.handleSourcesCheck)
	m.HandleFunc("POST /api/v1/sources/{name}/build", s.handleSourcesBuild)

	// Builds
	m.HandleFunc("GET /api/v1/builds", s.handleBuildsList)
	m.HandleFunc("GET /api/v1/builds/{id}", s.handleBuildGet)
	m.HandleFunc("POST /api/v1/builds/{id}/cancel", s.handleBuildCancel)

	// Runtimes
	m.HandleFunc("GET /api/v1/runtimes", s.handleRuntimesList)
	m.HandleFunc("DELETE /api/v1/runtimes/{id...}", s.handleRuntimeRemove)

	// Selections
	m.HandleFunc("GET /api/v1/selections", s.handleSelectionsGet)
	m.HandleFunc("PUT /api/v1/selections", s.handleSelectionsPut)

	// Models
	m.HandleFunc("GET /api/v1/models", s.handleModelsList)
	m.HandleFunc("POST /api/v1/models/scan", s.handleModelsScan)
	m.HandleFunc("GET /api/v1/models/sources", s.handleModelSourcesGet)
	m.HandleFunc("PUT /api/v1/models/sources", s.handleModelSourcesPut)
	m.HandleFunc("GET /api/v1/models/{id}/settings", s.handleModelSettingsGet)
	m.HandleFunc("PUT /api/v1/models/{id}/settings", s.handleModelSettingsPut)
	m.HandleFunc("DELETE /api/v1/models/{id}/settings", s.handleModelSettingsDelete)
	m.HandleFunc("POST /api/v1/models/{id}/load", s.handleModelLoad)
	m.HandleFunc("POST /api/v1/models/{id}/unload", s.handleModelUnload)
	m.HandleFunc("GET /api/v1/instances", s.handleInstances)

	// Events (SSE)
	m.HandleFunc("GET /api/v1/events", s.handleEvents)

	// OpenAI-compatible API
	m.HandleFunc("GET /v1/models", s.handleOpenAIModels)
	m.HandleFunc("POST /v1/chat/completions", s.handleOpenAIProxy)
	m.HandleFunc("POST /v1/completions", s.handleOpenAIProxy)
	m.HandleFunc("POST /v1/embeddings", s.handleOpenAIProxy)

	m.Handle("/", s.spaHandler(ui))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  version.String(),
		"root":     s.deps.Paths.Root,
		"hardware": s.hw(),
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg())
}

func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	if s.deps.UpdateConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "config updates unavailable")
		return
	}
	var body map[string]any
	if err := ReadJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := s.deps.UpdateConfig(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.deps.Supervisor != nil {
		if rng := cfg.Server.InternalPortRange; len(rng) == 2 {
			s.deps.Supervisor.SetPortRange(rng[0], rng[1])
		}
	}
	s.broadcast("config", cfg)
	writeJSON(w, http.StatusOK, cfg)
}

// spaHandler serves static assets and falls back to index.html so the
// single-page UI can own client-side routes. Missing assets are never
// rewritten to index.html, which would otherwise be served as HTML with
// a JavaScript content type and blank the app.
func (s *Server) spaHandler(ui fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		_, statErr := fs.Stat(ui, name)
		if statErr != nil {
			if strings.HasPrefix(name, "assets/") || strings.Contains(filepath.Base(name), ".") {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		if strings.HasPrefix(name, "assets/") {
			// Hashed asset names are content-addressed and safe to cache.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// index.html must always be revalidated so new asset hashes
			// are picked up after an upgrade.
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "runtimeforge_error"}})
}

// ReadJSON decodes a request body into v.
func ReadJSON(r *http.Request, v any) error {
	defer func() { _, _ = io.Copy(io.Discard, r.Body) }()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return nil
}
