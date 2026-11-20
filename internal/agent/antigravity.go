package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Google's Antigravity CLI (agy) runs command hooks from hooks.json files:
// a JSON object of named hooks, each a map of event to handlers. Its
// payloads are camelCase (conversationId, transcriptPath, toolCall) and its
// events have their own meanings, so FromAntigravity rewrites each one in
// Claude Code's vocabulary before anything else reads it.
//
// agy has no hook for a permission prompt or a question: the daemon reads
// those from the OSC 7501 reports agy writes to its terminal.

// AntigravityHookName is the key of pitwall's entry in agy's hooks.json.
const AntigravityHookName = "pitwall"

// antigravityEvents are the agy events pitwall registers. PreToolUse is
// left out on purpose: agy blocks a tool call whose PreToolUse hook fails,
// so a missing or broken pitwall binary would stop every command.
var antigravityEvents = []string{"SessionStart", "PreInvocation", "PostToolUse", "Stop"}

// AntigravityDir is where agy reads the user's hooks.json: .gemini/config
// in home. agy moves it only with HOME.
func AntigravityDir(home string) string { return filepath.Join(home, ".gemini", "config") }

// AntigravityHooks returns pitwall's named hook for agy's hooks.json, as
// {"pitwall": {event: handlers}}: `<bin> hook antigravity` on each event
// Derive reads, with agy's 5 second timeout. PostToolUse takes a matcher
// group, the others a flat list. Like ClaudeHooks, the command no-ops
// outside a pitwall pane.
func AntigravityHooks(bin string) []byte {
	type handler struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type group struct {
		Matcher string    `json:"matcher"`
		Hooks   []handler `json:"hooks"`
	}
	h := handler{Command: commandPath(runtime.GOOS, bin) + " hook antigravity", Timeout: 5}
	events := map[string]any{}
	for _, e := range antigravityEvents {
		if e == "PostToolUse" {
			events[e] = []group{{Matcher: "*", Hooks: []handler{h}}}
		} else {
			events[e] = []handler{h}
		}
	}
	b, _ := json.MarshalIndent(map[string]any{AntigravityHookName: events}, "", "  ")
	return b
}

// agyPayload is the part of an agy hook payload pitwall reads.
type agyPayload struct {
	Event          string `json:"hookEventName"`
	ConversationID string `json:"conversationId"`
	TranscriptPath string `json:"transcriptPath"`
	Cwd            string `json:"cwd"`
	LastUserInput  string `json:"lastUserInput"`
	InvocationNum  *int   `json:"invocationNum"`
	ToolCall       *struct {
		ID   string                     `json:"id"`
		Name string                     `json:"name"`
		Args map[string]json.RawMessage `json:"args"`
	} `json:"toolCall"`
	Error             string `json:"error"`
	TerminationReason string `json:"terminationReason"`
}

// FromAntigravity rewrites an agy hook payload as the Claude Code hook
// Derive and the rest of this package read, or returns nil for one that
// changes nothing:
//
//	SessionStart                    SessionStart (the session id and transcript)
//	PreInvocation, the turn's first UserPromptSubmit, prompt = lastUserInput
//	PreInvocation, later ones       nil: the turn is already working
//	PostToolUse                     PostToolUse, or PostToolUseFailure with
//	                                its error; run_command's CommandLine
//	                                is the command
//	Stop, terminationReason
//	  ERROR, MAX_TOKEN_BUDGET_EXCEEDED StopFailure, Detail = the error
//	  USER_CANCELED                 Interrupt (the activity goes)
//	  others                        Stop, with the reply the transcript's
//	                                last line holds as last_assistant_message
//
// conversationId becomes session_id and transcriptPath transcript_path.
func FromAntigravity(payload []byte) []byte {
	var a agyPayload
	if json.Unmarshal(payload, &a) != nil || a.Event == "" {
		return nil
	}
	out := map[string]any{"session_id": a.ConversationID, "cwd": a.Cwd}
	if t := agyTranscript(a.TranscriptPath); t != "" {
		out["transcript_path"] = t
	}
	switch a.Event {
	case "SessionStart":
		out["hook_event_name"] = "SessionStart"
	case "PreInvocation":
		if a.InvocationNum == nil || *a.InvocationNum != 0 {
			return nil
		}
		out["hook_event_name"], out["prompt"] = "UserPromptSubmit", a.LastUserInput
	case "PostToolUse":
		if a.ToolCall == nil {
			return nil
		}
		out["hook_event_name"] = "PostToolUse"
		if a.Error != "" {
			out["hook_event_name"], out["error"] = "PostToolUseFailure", a.Error
			// why: agy reports a command the user stopped with this error and nothing else.
			out["is_interrupt"] = strings.Contains(a.Error, "context canceled")
		}
		out["tool_name"], out["tool_use_id"] = a.ToolCall.Name, a.ToolCall.ID
		input := map[string]string{}
		if c := agyArg(a.ToolCall.Args, "CommandLine"); c != "" {
			input["command"] = c
		}
		for _, k := range []string{"TargetFile", "AbsolutePath", "Path", "File"} {
			if f := agyArg(a.ToolCall.Args, k); f != "" {
				input["file_path"] = f
				break
			}
		}
		out["tool_input"] = input
	case "Stop":
		switch a.TerminationReason {
		case "ERROR", "MAX_TOKEN_BUDGET_EXCEEDED":
			out["hook_event_name"], out["error"] = "StopFailure", firstNonEmpty(a.Error, a.TerminationReason)
		case "USER_CANCELED":
			out["hook_event_name"] = "Interrupt"
		default:
			out["hook_event_name"] = "Stop"
			out["last_assistant_message"] = agyLastReply(agyTranscript(a.TranscriptPath))
		}
	default:
		return nil
	}
	b, _ := json.Marshal(out)
	return b
}

// agyArg is a tool call's string argument k, "" when it is missing or not
// a string.
func agyArg(args map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(args[k], &s)
	return s
}

// agyTranscript is path when it names an agy transcript, an absolute
// transcript_full.jsonl or transcript.jsonl, else "".
func agyTranscript(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	if b := filepath.Base(path); b != "transcript_full.jsonl" && b != "transcript.jsonl" {
		return ""
	}
	return filepath.Clean(path)
}

// agyTail is how much of a transcript's end agyLastReply reads.
const agyTail = 256 << 10

// agyLastReply is the text of the last model reply in an agy transcript:
// the content of its last PLANNER_RESPONSE line that has one, within the
// file's last agyTail bytes. agy writes the reply before it runs Stop.
func agyLastReply(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > agyTail {
		f.Seek(info.Size()-agyTail, io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(f, agyTail))
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var l struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal(lines[i], &l) == nil && l.Type == "PLANNER_RESPONSE" && strings.TrimSpace(l.Content) != "" {
			return l.Content
		}
	}
	return ""
}
