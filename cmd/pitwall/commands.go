package main

import "github.com/quanticstudios/pitwall/internal/proto"

func (b *backend) CommandResults() []proto.CommandResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.cmds
	b.cmds = nil
	return r
}

func (m *multi) CommandResults() []proto.CommandResult {
	var out []proto.CommandResult
	for _, h := range m.hosts {
		for _, r := range h.b.CommandResults() {
			out = append(out, r)
		}
	}
	return out
}

func (b *backend) PromptMarks(pane string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.frames[pane].PromptMarks
}

func (m *multi) PromptMarks(pane string) bool {
	if h := m.of(pane); h != nil {
		return h.b.PromptMarks(pane)
	}
	return false
}
