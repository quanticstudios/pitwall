package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
)

// runGuard is `pitwall guard`, which pitwall's pi extension and OpenCode
// plugin run before a shell command in a guarded tab. It reads
// {"tool_input":{"command":...}} on stdin and returns 2 when the guard
// denies the command, 0 otherwise, an unreadable event included.
func runGuard(stdin io.Reader, stdout, stderr io.Writer) int {
	var ev struct {
		ToolInput json.RawMessage `json:"tool_input"`
	}
	data, _ := io.ReadAll(io.LimitReader(stdin, 16<<20))
	if json.Unmarshal(data, &ev) != nil {
		return 0
	}
	return guardCheck(ev.ToolInput, stdout, stderr)
}

// guardHook is the guard in `pitwall hook`: in a tab with guard.Env set,
// it denies a Claude Code or Codex PreToolUse whose shell command the guard
// denies, and returns 2. Otherwise it prints nothing and returns 0, and the
// hook reports as always.
func guardHook(provider model.Provider, payload []byte, stdout, stderr io.Writer) int {
	if os.Getenv(guard.Env) == "" || provider != model.ProviderClaude && provider != model.ProviderCodex {
		return 0
	}
	var ev struct {
		Event     string          `json:"hook_event_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(payload, &ev) != nil || ev.Event != "PreToolUse" {
		return 0
	}
	return guardCheck(ev.ToolInput, stdout, stderr)
}

// Adapted from forge (MIT): cmd/forge/guard.go

// guardCheck checks one tool call's input against the guard's list and
// [agents] guard_deny. A denied call gets the PreToolUse deny that Claude
// Code and Codex read on stdout, its reason on stderr, and 2.
func guardCheck(toolInput json.RawMessage, stdout, stderr io.Writer) int {
	if len(toolInput) == 0 {
		return 0
	}
	// invariant: a config with problems still loads what it can; guard_deny only adds to guard.Deny.
	s, _ := config.Load()
	cmd := guard.Check(s.GuardDeny, toolInput)
	if cmd == "" {
		return 0
	}
	reason := guard.Reason(cmd)
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason,
	}})
	fmt.Fprintln(stdout, string(out))
	// why: exit 2 with the reason on stderr is the deny both agents honor should one ignore the JSON.
	fmt.Fprintln(stderr, reason)
	return 2
}
