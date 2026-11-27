package remote

import (
	"log"
	"net/http"
	"slices"

	"github.com/quanticstudios/pitwall/internal/logs"
)

// Keys are the keys the phone's key bar presses, as pitwall send-key names
// them; POST /api/key refuses any other.
var Keys = []string{"esc", "enter", "tab", "shift+tab", "up", "down", "left", "right", "ctrl+c",
	"1", "2", "3", "4", "5", "6", "7", "8", "9"}

// screen serves ?pane='s screen, with what looks like a secret redacted.
func (s *Server) screen(w http.ResponseWriter, r *http.Request, d Device) {
	pane := r.URL.Query().Get("pane")
	if pane == "" {
		http.Error(w, "name the pane", http.StatusBadRequest)
		return
	}
	text, err := s.Screen(pane)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON200(w, map[string]any{"text": logs.Redact(text)})
}

// key presses one of Keys in a pane.
func (s *Server) key(w http.ResponseWriter, r *http.Request, d Device) {
	var req struct {
		Pane string `json:"pane"`
		Key  string `json:"key"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Pane == "" || !slices.Contains(Keys, req.Key) {
		http.Error(w, "name the pane and a key the key bar offers", http.StatusBadRequest)
		return
	}
	if err := s.Key(req.Pane, req.Key); err != nil {
		log.Printf("remote: %q: key %s to pane %s refused: %q", d.Name, req.Key, req.Pane, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	log.Printf("remote: %q: key %s sent to pane %s", d.Name, req.Key, req.Pane)
	writeJSON200(w, map[string]any{})
}

// stop interrupts a pane's agent's turn.
func (s *Server) stop(w http.ResponseWriter, r *http.Request, d Device) {
	var req struct {
		Pane string `json:"pane"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.Stop(req.Pane); err != nil {
		log.Printf("remote: %q: stop of pane %s refused: %q", d.Name, req.Pane, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	log.Printf("remote: %q: stop sent to pane %s", d.Name, req.Pane)
	writeJSON200(w, map[string]any{})
}
