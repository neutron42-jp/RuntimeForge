package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"runtimeforge/internal/build"
)

// broadcast sends an event to every SSE subscriber.
func (s *Server) broadcast(eventType string, payload any) {
	envelope := map[string]any{
		"type": eventType,
		"time": time.Now().UTC(),
		"data": payload,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	s.mu.Lock()
	chans := make([]chan []byte, 0, len(s.subs))
	for _, c := range s.subs {
		chans = append(chans, c)
	}
	s.mu.Unlock()
	for _, c := range chans {
		select {
		case c <- data:
		default:
		}
	}
}

func (s *Server) forwardBuildEvents(engine *build.Engine) {
	ch, _ := engine.Subscribe()
	for ev := range ch {
		s.broadcast(ev.Type, ev)
	}
}

func (s *Server) subscribe() chan []byte {
	ch := make(chan []byte, 128)
	s.mu.Lock()
	id := s.subID
	s.subID++
	s.subs[id] = ch
	s.mu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, c := range s.subs {
		if c == ch {
			delete(s.subs, id)
			close(c)
			return
		}
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
