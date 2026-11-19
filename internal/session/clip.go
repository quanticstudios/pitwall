package session

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ClipBytes is the most of one text field an item carries in a snapshot
// or page; ChatRead Item fetches the rest.
const ClipBytes = 16 << 10

// Clip is it with each text longer than limit bytes cut to its head and
// tail around a line saying how many lines it left out, the long strings
// of a tool's input cut alike, its images and its result's over limit
// without their data, and its subagent's calls cut as it is. Cut
// reports what it left out. A tool's output keeps more of its tail, which
// the transcript shows first.
func Clip(it Item, limit int) Item {
	cut := false
	clip := func(s string) string {
		c, ok := clipText(s, limit)
		cut = cut || ok
		return c
	}
	atts := func(as []Attachment) []Attachment {
		if len(as) == 0 {
			return as
		}
		out := make([]Attachment, len(as))
		for i, a := range as {
			if len(a.Data) > limit {
				a.Data, cut = "", true
			}
			a.Text = clip(a.Text)
			out[i] = a
		}
		return out
	}
	var tool func(t ToolCall) ToolCall
	tool = func(t ToolCall) ToolCall {
		t.Output, t.Diff, t.Progress = clip(t.Output), clip(t.Diff), clip(t.Progress)
		t.Images = atts(t.Images)
		if len(t.Input) > limit {
			var in map[string]any
			if json.Unmarshal(t.Input, &in) == nil {
				for k, v := range in {
					if s, ok := v.(string); ok {
						in[k] = clip(s)
					}
				}
				t.Input, _ = json.Marshal(in)
			}
		}
		if len(t.SubagentCalls) > 0 {
			calls := make([]ToolCall, len(t.SubagentCalls))
			for i, c := range t.SubagentCalls {
				calls[i] = tool(c)
			}
			t.SubagentCalls = calls
		}
		return t
	}
	it.Text = clip(it.Text)
	it.Attachments = atts(it.Attachments)
	if it.Tool != nil {
		t := tool(*it.Tool)
		it.Tool = &t
	}
	it.Cut = it.Cut || cut
	return it
}

// clipText is s cut to limit bytes, a quarter of them its head and the
// rest its tail, at line ends where it can, with a line between saying
// how many lines are left out; false when s fits.
func clipText(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	head := s[:limit/4]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i+1]
	}
	tail := s[len(s)-(limit-len(head)):]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	n := strings.Count(s[len(head):len(s)-len(tail)], "\n")
	head, tail = strings.ToValidUTF8(head, ""), strings.ToValidUTF8(tail, "")
	if !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	return head + "… " + strconv.Itoa(n) + " more lines …\n" + tail, true
}
