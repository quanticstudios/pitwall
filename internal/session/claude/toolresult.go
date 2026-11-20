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
	TaskID           string `json:"taskId"` // Monitor: the watch it started
	Type             string `json:"type"`   // Write: create or update
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
				s.bg, s.tasks = map[string]session.ToolCall{}, map[string]string{}
			}
			s.bg[t.CallID] = *t
			task := r.BackgroundTaskID
			if m := backgroundID.FindStringSubmatch(t.Output); task == "" && m != nil {
				task = m[1]
			}
			s.tasks[task] = t.CallID
		case t.Status == "failed":
			if m := exitCodeLine.FindStringSubmatch(t.Output); m != nil {
				n, _ := strconv.Atoi(m[1])
				t.ExitCode = &n
			}
		case !r.Interrupted:
			t.ExitCode = new(int) // Claude Code fails a call that exits non-zero
		}
	case "Monitor":
		// A watch runs on like a background shell: its notices end it, and
		// its events go on its row.
		if r.TaskID != "" && t.Status == "completed" {
			t.Background = "running"
			if s.bg == nil {
				s.bg, s.tasks = map[string]session.ToolCall{}, map[string]string{}
			}
			s.bg[t.CallID] = *t
			s.tasks[r.TaskID] = t.CallID
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

// notice is a task notification's text, which Claude Code adds as a user
// line or queues when a background shell ends: the shell's call with its
// state.
func (s *stream) notice(text string) []session.Event {
	id, st := noticeTool.FindStringSubmatch(text), noticeStatus.FindStringSubmatch(text)
	if id == nil || st == nil {
		return nil
	}
	return s.background(id[1], st[1])
}

// background is background shell call id in state: running, completed,
// failed or killed. A call that is not a background shell gives nothing.
func (s *stream) background(id, state string) []session.Event {
	t, ok := s.bg[id]
	state = strings.TrimSpace(state)
	if !ok || state == "" || state == t.Background {
		return nil
	}
	t.Background = state
	if state == "running" || state == "pending" {
		t.Background = "running"
	}
	if cur, ok := s.tools[id]; ok {
		cur.Background = t.Background
		s.tools[id] = cur
		t = cur
	}
	switch {
	case t.Background == "running", t.ToolName == "Monitor":
		// A watch's last events can come after its end notice.
		s.bg[id] = t
	default:
		delete(s.bg, id)
	}
	return []session.Event{{Type: session.EventContent, ItemID: id, Kind: session.KindToolCall, Tool: &t}}
}

// monitorEvent is a Monitor watch's event, which Claude Code notifies as
// it happens: a line more of its call's output, after the line that
// started it.
func (s *stream) monitorEvent(task, event string) []session.Event {
	id := s.tasks[task]
	t, ok := s.bg[id]
	event = strings.TrimSpace(event)
	if !ok || event == "" {
		return nil
	}
	if cur, ok := s.tools[id]; ok {
		t = cur
	}
	t.Output = strings.TrimRight(t.Output, "\n") + "\n" + event
	s.bg[id] = t
	if _, ok := s.tools[id]; ok {
		s.tools[id] = t
	}
	return []session.Event{{Type: session.EventContent, ItemID: id, Kind: session.KindToolCall, Tool: &t}}
}
