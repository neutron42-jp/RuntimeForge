package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"runtimeforge/internal/models"
	"runtimeforge/internal/supervisor"
)

// handleOpenAIModels lists the models that can currently be served.
// Loaded models are listed individually; the sentinel "loaded" resolves
// to whatever is loaded (SPEC §14).
func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	if s.deps.Supervisor != nil {
		for _, inst := range s.deps.Supervisor.List() {
			data = append(data, map[string]any{
				"id":       inst.ModelID,
				"object":   "model",
				"owned_by": "runtimeforge",
				"state":    inst.Status,
			})
		}
	}
	data = append(data, map[string]any{
		"id":       "loaded",
		"object":   "model",
		"owned_by": "runtimeforge",
	})
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// handleOpenAIProxy forwards an OpenAI-style request to the loaded
// model's llama-server. The request "model" field may be a loaded model
// id/name or the sentinel "loaded" (or absent).
func (s *Server) handleOpenAIProxy(w http.ResponseWriter, r *http.Request) {
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor available")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	requested, _ := body["model"].(string)

	inst, status, err := s.resolveInstance(requested)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}

	// llama-server ignores the model name, but normalize it so logs and
	// responses are coherent.
	body["model"] = inst.ModelName
	buf, err := json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	r.Body = newReadCloser(buf)
	r.ContentLength = int64(len(buf))
	r.Header.Set("Content-Length", fmt.Sprintf("%d", len(buf)))

	target, err := url.Parse(inst.BaseURL())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
	}
	proxy.ServeHTTP(w, r)
}

// resolveInstance finds the instance to serve a request.
func (s *Server) resolveInstance(requested string) (supervisor.Instance, int, error) {
	var known []models.Model
	if s.deps.Models != nil {
		known = s.deps.Models.List()
	}
	return selectInstance(s.deps.Supervisor.List(), known, requested)
}

// selectInstance implements the "loaded" sentinel and explicit model
// routing described in SPEC §14.
func selectInstance(instances []supervisor.Instance, known []models.Model, requested string) (supervisor.Instance, int, error) {
	ready := make([]supervisor.Instance, 0, len(instances))
	for _, i := range instances {
		if i.Status == supervisor.StateReady {
			ready = append(ready, i)
		}
	}

	if requested == "" || strings.EqualFold(requested, "loaded") {
		if len(ready) == 0 {
			if len(instances) > 0 {
				return supervisor.Instance{}, http.StatusConflict, fmt.Errorf("model is still loading; retry shortly")
			}
			return supervisor.Instance{}, http.StatusConflict, fmt.Errorf("no model is loaded; load one explicitly first")
		}
		best := ready[0]
		for _, i := range ready[1:] {
			if i.StartedAt.After(best.StartedAt) {
				best = i
			}
		}
		return best, 0, nil
	}

	for _, i := range instances {
		if i.ModelID == requested || strings.EqualFold(i.ModelName, requested) {
			if i.Status != supervisor.StateReady {
				return supervisor.Instance{}, http.StatusConflict, fmt.Errorf("model %q is %s", requested, i.Status)
			}
			return i, 0, nil
		}
	}

	for _, m := range known {
		if m.ID == requested || strings.EqualFold(m.Name, requested) {
			return supervisor.Instance{}, http.StatusConflict,
				fmt.Errorf("model %q is registered but not loaded; load it explicitly first", requested)
		}
	}
	return supervisor.Instance{}, http.StatusNotFound, fmt.Errorf("unknown model %q", requested)
}

func newReadCloser(b []byte) *readCloser {
	return &readCloser{Reader: bytes.NewReader(b)}
}

type readCloser struct{ *bytes.Reader }

func (r *readCloser) Close() error { return nil }
