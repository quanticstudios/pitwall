package store

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/model"
)

// HistoryBytes limits the uncompressed text saved for one pane.
const HistoryBytes = 4 << 20

// PackHistory compresses text and SGR colors for the state file and snapshots.
// Adapted from tuios (MIT): internal/session/scrollback_persist.go
func PackHistory(cols int, text string, at time.Time) (*model.PaneHistory, error) {
	if text == "" {
		return nil, nil
	}
	if cols < 1 || cols > 1000 || len(text) > HistoryBytes {
		return nil, errors.New("pane history exceeds size limit")
	}
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, text); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return &model.PaneHistory{Cols: cols, SavedAt: at, Data: buf.Bytes()}, nil
}

// UnpackHistory rejects oversized or damaged history without losing the tab.
func UnpackHistory(h *model.PaneHistory) (string, error) {
	if h == nil {
		return "", nil
	}
	if h.Cols < 1 || h.Cols > 1000 || len(h.Data) > HistoryBytes {
		return "", errors.New("invalid pane history size")
	}
	r, err := gzip.NewReader(bytes.NewReader(h.Data))
	if err != nil {
		return "", err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, HistoryBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > HistoryBytes {
		return "", errors.New("pane history exceeds size limit")
	}
	if !historyTextValid(string(data)) {
		return "", errors.New("pane history contains non-SGR controls")
	}
	return string(data), nil
}

// historyTextValid allows text and SGR only, never terminal queries or OSC commands.
func historyTextValid(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for len(text) > 0 {
		if text[0] == '\x1b' {
			if len(text) < 3 || text[1] != '[' {
				return false
			}
			i := 2
			for i < len(text) && i < 128 && (text[i] >= '0' && text[i] <= '9' || text[i] == ';') {
				i++
			}
			if i >= len(text) || text[i] != 'm' {
				return false
			}
			text = text[i+1:]
			continue
		}
		r, n := utf8.DecodeRuneInString(text)
		if unicode.IsControl(r) && r != '\r' && r != '\n' {
			return false
		}
		text = text[n:]
	}
	return true
}
