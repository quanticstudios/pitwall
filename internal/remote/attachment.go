package remote

import (
	"errors"
	"io"
	"net/http"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func (s *Server) upload(w http.ResponseWriter, r *http.Request, _ Device) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, proto.MaxPasteImage))
	if err != nil {
		var cap *http.MaxBytesError
		code := http.StatusBadRequest
		if errors.As(err, &cap) {
			code = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "image upload failed or exceeds 20 MiB", code)
		return
	}
	path, err := s.Upload(r.URL.Query().Get("pane"), data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON200(w, map[string]string{"image": path})
}

func (s *Server) question(w http.ResponseWriter, r *http.Request, _ Device) {
	var req struct {
		Pane   string `json:"pane"`
		ID     string `json:"id"`
		Option int    `json:"option"`
		Text   string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.Question(req.Pane, req.ID, req.Option, req.Text); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON200(w, map[string]any{})
}
