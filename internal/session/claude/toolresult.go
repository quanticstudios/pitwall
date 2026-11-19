package claude

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// maxImages is how many pictures a call's result keeps.
// shortcut: the rest of a result's images are dropped; carry them all if a
// tool that returns many (a gallery, a PDF's pages) needs to show them.
const maxImages = 4

var (
	exitCodeLine = regexp.MustCompile(`^Exit code (-?\d+)`)
	backgroundID = regexp.MustCompile(`running in background with ID: ([A-Za-z0-9_-]+)`)
	noticeTool   = regexp.MustCompile(`<tool-use-id>([^<]+)</tool-use-id>`)
	noticeStatus = regexp.MustCompile(`<status>([^<]+)</status>`)
)

// toolUseResult is the fields of the CLI's own result beside a
// tool_result that the chat shows.
type toolUseResult struct {
	Interrupted      bool   `json:"interrupted"`
	BackgroundTaskID string `json:"backgroundTaskId"`
	Type             string `json:"type"` // Write: create or update
	FilePath         string `json:"filePath"`
	StructuredPatch  []struct {
		OldStart int      `json:"oldStart"`
		OldLines int      `json:"oldLines"`
		NewStart int      `json:"newStart"`
		NewLines int      `json:"newLines"`
		Lines    []string `json:"lines"`
	} `json:"structuredPatch"`
}

// finish fills in what a finished call's result says beyond its text: the
// pictures it returned, a shell's exit code or the shell it left running,
// and an edit's patch with its line numbers.
func (s *stream) finish(t *session.ToolCall, b block, raw json.RawMessage) {
	t.Images = resultImages(b.Content)
	var r toolUseResult
	_ = json.Unmarshal(raw, &r) // a string for errors and rejections
	switch t.ToolName {
	case "Bash":
		var in struct {
			Background bool `json:"run_in_background"`
		}
		_ = json.Unmarshal(t.Input, &in)
		switch {
		case r.BackgroundTaskID != "" || in.Background && backgroundID.MatchString(t.Output):
			t.Background = "running"
			if s.bg == nil {
				s.bg = map[string]session.ToolCall{}
			}
			s.bg[t.CallID] = *t
		case t.Status == "failed":
			if m := exitCodeLine.FindStringSubmatch(t.Output); m != nil {
				n, _ := strconv.Atoi(m[1])
				t.ExitCode = &n
			}
		case !r.Interrupted:
			t.ExitCode = new(int) // Claude Code fails a call that exits non-zero
		}
	case "Edit", "MultiEdit", "Write":
		var d strings.Builder
		for _, h := range r.StructuredPatch {
			d.WriteString("@@ -" + strconv.Itoa(h.OldStart) + "," + strconv.Itoa(h.OldLines) +
				" +" + strconv.Itoa(h.NewStart) + "," + strconv.Itoa(h.NewLines) + " @@\n")
			for _, l := range h.Lines {
				d.WriteString(l + "\n")
			}
		}
		t.Diff = d.String()
		if r.Type == "create" && r.FilePath != "" {
			t.Files = []session.ToolFile{{Path: r.FilePath, Status: "add"}}
		}
	}
}

// resultImages are a tool_result's image blocks as attachments.
func resultImages(c json.RawMessage) []session.Attachment {
	var blocks []struct {
		Type   string `json:"type"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if json.Unmarshal(c, &blocks) != nil {
		return nil
	}
	var out []session.Attachment
	for _, b := range blocks {
		if b.Type != "image" || b.Source.Type != "base64" || b.Source.Data == "" || len(out) == maxImages {
			continue
		}
		out = append(out, session.Attachment{ID: "image-" + strconv.Itoa(len(out)+1), Kind: "image",
			MimeType: b.Source.MediaType, Data: b.Source.Data})
	}
	return out
}

// notice is a task notification, a user line Claude Code adds when a
// background shell ends: the shell's call with its state.
func (s *stream) notice(text string) []session.Event {
	id, st := noticeTool.FindStringSubmatch(text), noticeStatus.FindStringSubmatch(text)
	if id == nil || st == nil {
		return nil
	}
	t, ok := s.bg[id[1]]
	if !ok {
		return nil
	}
	t.Background = strings.TrimSpace(st[1])
	if t.Background != "running" {
		delete(s.bg, id[1])
	}
	if cur, ok := s.tools[id[1]]; ok {
		cur.Background = t.Background
		s.tools[id[1]] = cur
		t = cur
	}
	return []session.Event{{Type: session.EventContent, ItemID: id[1], Kind: session.KindToolCall, Tool: &t}}
}
