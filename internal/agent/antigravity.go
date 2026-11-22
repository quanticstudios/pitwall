package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
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
	Event          string  `json:"hookEventName"`
	ConversationID string  `json:"conversationId"`
	ArtifactDir    string  `json:"artifactDirectoryPath"`
	TranscriptPath string  `json:"transcriptPath"`
	Cwd            string  `json:"cwd"`
	LastUserInput  *string `json:"lastUserInput"`
	InvocationNum  *int    `json:"invocationNum"`
	ToolCall       *struct {
		ID   string                     `json:"id"`
		Name string                     `json:"name"`
		Args map[string]json.RawMessage `json:"args"`
	} `json:"toolCall"`
	Error             string `json:"error"`
	TerminationReason string `json:"terminationReason"`
	FullyIdle         *bool  `json:"fullyIdle"`
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
//	                                last line holds as last_assistant_message;
//	                                fullyIdle=false marks background subagent
//	                                work active so the pane stays working
//
// Subagent hooks (proved by agy's on-disk parent records or by an explicit
// empty lastUserInput) never carry session_id or transcript_path: SessionStart,
// PreInvocation and Stop return nil, and PostToolUse carries agent_id so the
// daemon counts it as activity without changing session, transcript or state.
//
// conversationId becomes session_id and transcriptPath transcript_path.
func FromAntigravity(payload []byte) []byte {
	var a agyPayload
	if json.Unmarshal(payload, &a) != nil || a.Event == "" {
		return nil
	}
	if agyIsSubagent(a) {
		if a.Event != "PostToolUse" || a.ToolCall == nil {
			return nil
		}
		out := map[string]any{
			"hook_event_name": "PostToolUse",
			"agent_id":        firstNonEmpty(a.ConversationID, "subagent"),
			"tool_name":       a.ToolCall.Name,
			"tool_use_id":     a.ToolCall.ID,
			"tool_input":      agyToolInput(a.ToolCall.Args),
		}
		if a.Error != "" {
			out["hook_event_name"], out["error"] = "PostToolUseFailure", a.Error
			out["is_interrupt"] = strings.Contains(a.Error, "context canceled")
		}
		b, _ := json.Marshal(out)
		return b
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
		prompt := ""
		if a.LastUserInput != nil {
			prompt = *a.LastUserInput
		}
		out["hook_event_name"], out["prompt"] = "UserPromptSubmit", prompt
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
		out["tool_input"] = agyToolInput(a.ToolCall.Args)
	case "Stop":
		switch a.TerminationReason {
		case "ERROR", "MAX_TOKEN_BUDGET_EXCEEDED":
			out["hook_event_name"], out["error"] = "StopFailure", firstNonEmpty(a.Error, a.TerminationReason)
		case "USER_CANCELED":
			out["hook_event_name"] = "Interrupt"
		default:
			out["hook_event_name"] = "Stop"
			out["last_assistant_message"] = agyLastReply(agyTranscript(a.TranscriptPath))
			if a.FullyIdle != nil && !*a.FullyIdle {
				out["background_tasks"] = []map[string]string{{"type": "subagent", "status": "running"}}
			}
		}
	default:
		return nil
	}
	b, _ := json.Marshal(out)
	return b
}

// agyToolInput extracts command and file_path from an agy tool call's args.
func agyToolInput(args map[string]json.RawMessage) map[string]string {
	input := map[string]string{}
	if c := agyArg(args, "CommandLine"); c != "" {
		input["command"] = c
	}
	for _, k := range []string{"TargetFile", "AbsolutePath", "Path", "File"} {
		if f := agyArg(args, k); f != "" {
			input["file_path"] = f
			break
		}
	}
	return input
}

// agyIsSubagent reports whether an agy hook payload came from a subagent
// conversation. agy's hook JSON carries no parent or agent field, so two
// signals prove it: on disk, agy writes the parent link under
// brain/<subId>/.system_generated/messages/ (and
// brain/<parent>/.system_generated/subagents/<subId>.json) before firing
// SessionStart; in the payload itself, subagents receive a SYSTEM_MESSAGE
// rather than USER_INPUT so lastUserInput is explicitly "".
func agyIsSubagent(a agyPayload) bool {
	if a.ConversationID != "" {
		if brain := agyBrainFromPaths(a.ArtifactDir, a.TranscriptPath); brain != "" && AgyParent(brain, a.ConversationID) != "" {
			return true
		}
	}
	return a.LastUserInput != nil && *a.LastUserInput == ""
}

// AgyBrainDir is where agy stores its per-conversation directories under home.
func AgyBrainDir(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain")
}

// AgyBrainOfTranscript returns the brain directory containing an agy
// transcript path (.../brain/<conv>/.system_generated/logs/transcript[_full].jsonl),
// or "" if path does not have that layout.
func AgyBrainOfTranscript(path string) string {
	return agyBrainFromPaths("", path)
}

// agyBrainFromPaths returns agy's brain directory from a hook's
// artifactDirectoryPath or transcriptPath, or "".
func agyBrainFromPaths(artifactDir, transcriptPath string) string {
	if filepath.IsAbs(artifactDir) {
		p := filepath.Dir(filepath.Clean(artifactDir))
		if filepath.Base(p) == "brain" {
			return p
		}
	}
	if t := agyTranscript(transcriptPath); t != "" {
		logs := filepath.Dir(t)
		sys := filepath.Dir(logs)
		conv := filepath.Dir(sys)
		brain := filepath.Dir(conv)
		if filepath.Base(logs) == "logs" && filepath.Base(sys) == ".system_generated" && filepath.Base(brain) == "brain" {
			return brain
		}
	}
	return ""
}

// agyValidConvID reports whether id is a safe single path element.
func agyValidConvID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

// agyHasSubagentFile reports whether parent's brain folder records conv as a
// subagent in .system_generated/subagents/<conv>.json.
func agyHasSubagentFile(brainDir, parent, conv string) bool {
	if !agyValidConvID(parent) || !agyValidConvID(conv) || parent == conv {
		return false
	}
	info, err := os.Stat(filepath.Join(brainDir, parent, ".system_generated", "subagents", conv+".json"))
	return err == nil && !info.IsDir()
}

// AgyParent returns the immediate parent conversation id of conv under
// brainDir, or "" when conv is a top-level conversation or no on-disk parent
// evidence exists. It checks conv's .system_generated/messages/*.json for a
// parent spawn message (invoke_subagent, "Message from Parent Agent", or a
// matching parent subagents/<conv>.json), with a fallback to step 0's
// SYSTEM_MESSAGE sender verified against parent's subagents/<conv>.json.
func AgyParent(brainDir, conv string) string {
	if brainDir == "" || !agyValidConvID(conv) {
		return ""
	}
	msgDir := filepath.Join(brainDir, conv, ".system_generated", "messages")
	if entries, err := os.ReadDir(msgDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".json") || name == "read.json" {
				continue
			}
			b, err := os.ReadFile(filepath.Join(msgDir, name))
			if err != nil {
				continue
			}
			var msg struct {
				Recipient     string `json:"recipient"`
				Sender        string `json:"sender"`
				RenderDetails struct {
					MessageTitle string `json:"messageTitle"`
				} `json:"renderDetails"`
				SourceMetadata struct {
					Tool struct {
						ConversationID string `json:"conversationId"`
						ToolCall       struct {
							Name string `json:"name"`
						} `json:"toolCall"`
					} `json:"tool"`
				} `json:"sourceMetadata"`
			}
			if json.Unmarshal(b, &msg) != nil || msg.Recipient != conv {
				continue
			}
			parent := firstNonEmpty(msg.SourceMetadata.Tool.ConversationID, msg.Sender)
			if !agyValidConvID(parent) || parent == conv {
				continue
			}
			if msg.SourceMetadata.Tool.ToolCall.Name == "invoke_subagent" ||
				msg.RenderDetails.MessageTitle == "Message from Parent Agent" ||
				agyHasSubagentFile(brainDir, parent, conv) {
				return parent
			}
		}
	}
	for _, name := range []string{"transcript_full.jsonl", "transcript.jsonl"} {
		f, err := os.Open(filepath.Join(brainDir, conv, ".system_generated", "logs", name))
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(f, 64<<10))
		f.Close()
		line, _, _ := bytes.Cut(data, []byte("\n"))
		var step0 struct {
			StepIndex int    `json:"step_index"`
			Type      string `json:"type"`
			Content   string `json:"content"`
		}
		if json.Unmarshal(line, &step0) != nil || step0.StepIndex != 0 || step0.Type != "SYSTEM_MESSAGE" {
			continue
		}
		if _, rest, ok := strings.Cut(step0.Content, " sender="); ok {
			parent, _, _ := strings.Cut(rest, " ")
			if agyHasSubagentFile(brainDir, parent, conv) {
				return parent
			}
		}
	}
	return ""
}

// AgyMainConversation walks agy's parent links from conv under home to the
// root main conversation id, or returns "" when conv has no parent.
func AgyMainConversation(home, conv string) string {
	brain := AgyBrainDir(home)
	if brain == "" || !agyValidConvID(conv) {
		return ""
	}
	cur := conv
	seen := map[string]bool{conv: true}
	for range 16 {
		p := AgyParent(brain, cur)
		if p == "" || seen[p] {
			break
		}
		seen[p] = true
		cur = p
	}
	if cur == conv {
		return ""
	}
	return cur
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

// agyFooterModes and agyFooterEfforts are what agy 1.5.13's footer can
// say: no mode word in its default mode.
var (
	agyFooterModes   = map[string]bool{"auto": true, "plan": true, "accept-edits": true}
	agyFooterEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
)

// AgyFooter reads the right end of agy's footer, "auto · Gemini 3.8 Flash
// · low": the mode ("default" when it names none), the model's name and the
// effort. ok is false for a screen without that footer, as while a picker
// covers it.
func AgyFooter(g vt.Grid) (mode, model, effort string, ok bool) {
	for _, l := range bottomLines(g, 2) {
		i := strings.LastIndex(l, "  ")
		if i < 0 {
			continue
		}
		parts := strings.Split(strings.TrimSpace(l[i:]), " · ")
		switch len(parts) {
		case 2:
			mode, model, effort = "default", parts[0], parts[1]
		case 3:
			mode, model, effort = parts[0], parts[1], parts[2]
			if !agyFooterModes[mode] {
				continue
			}
		default:
			continue
		}
		if model != "" && agyFooterEfforts[effort] {
			return mode, model, effort, true
		}
	}
	return "", "", "", false
}

// AntigravityTranscript is where agy 1.5.13 under home keeps conversation
// conv's transcript, as its hooks name it, or "" for an id that is no
// single path element. A pane resumed with --conversation conv runs that
// conversation, so the daemon names its transcript before agy's first hook,
// which comes only with the next prompt.
func AntigravityTranscript(home, conv string) string {
	if conv == "" || conv == "." || conv == ".." || strings.ContainsAny(conv, `/\`) {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain", conv, ".system_generated", "logs", "transcript_full.jsonl")
}

// AgyAlwaysOffered reports whether agy's prompt on g offers to allow a
// command for the rest of the conversation, its option 2. Its folder-trust
// question has no such row: there, 2 is "No, exit".
func AgyAlwaysOffered(g vt.Grid) bool {
	return strings.Contains(ScreenText(g, screenLines), "2. Yes, and always allow in this conversation")
}

// AgySettings is agy's own settings file under home, where it records the
// folders the user trusted, as {"trustedWorkspaces": [absolute paths]}.
func AgySettings(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// AgyTrust returns agy's settings, data, with dir added to its trusted
// folders, written as agy writes the file; ok is false, and data stays
// as it is, when dir is trusted already or main is not. agy trusts no
// folder for its parent's sake, so a worktree under a trusted repo asks
// again; this trusts it only for a repo the user trusted in agy.
func AgyTrust(data []byte, main, dir string) (out []byte, ok bool, err error) {
	root := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &root); err != nil {
		return data, false, err
	}
	var trusted []string
	if raw, has := root["trustedWorkspaces"]; has {
		if err := json.Unmarshal(raw, &trusted); err != nil {
			return data, false, err
		}
	}
	dir = filepath.Clean(dir)
	has := func(path string) bool {
		return slices.ContainsFunc(trusted, func(t string) bool { return samePath(t, path) })
	}
	if !has(main) || has(dir) {
		return data, false, nil
	}
	root["trustedWorkspaces"], _ = json.Marshal(append(trusted, dir))
	out, err = json.MarshalIndent(root, "", "  ")
	return append(out, '\n'), err == nil, err
}

// WorktreeMain is the main worktree of the linked git worktree dir lies
// in, read from the gitdir line of the worktree's .git file
// ("gitdir: <main>/.git/worktrees/<name>"), or "" when dir is in none:
// the main worktree itself, no repo, or a layout git worktree add did not
// make. It reads files only, so it is cheap where a git process is not.
func WorktreeMain(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		info, err := os.Lstat(filepath.Join(d, ".git"))
		switch {
		case err == nil && info.IsDir():
			return "" // a main worktree
		case err == nil && info.Mode().IsRegular():
			b, err := os.ReadFile(filepath.Join(d, ".git"))
			if err != nil {
				return ""
			}
			gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
			if !ok {
				return ""
			}
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(d, gitdir)
			}
			main, _, ok := strings.Cut(filepath.ToSlash(filepath.Clean(gitdir)), "/.git/worktrees/")
			if !ok {
				return ""
			}
			return filepath.FromSlash(main)
		}
		if parent := filepath.Dir(d); parent == d {
			return ""
		}
	}
}

// AgyTrustAsked reports agy's question whether to trust the folder it
// opened in, on g.
func AgyTrustAsked(g vt.Grid) bool {
	return strings.Contains(ScreenText(g, screenLines), "Do you trust the contents of this project?")
}

// agyMenuHint reports whether g's bottom rows show the keyboard footer of an
// Antigravity CLI picker, menu or dialog, such as /model, /settings,
// onboarding, folder trust, permission or question prompts. Mid-line, the
// words are only someone quoting them.
func agyMenuHint(g vt.Grid) bool {
	for _, l := range bottomLines(g, screenLines) {
		low := strings.ToLower(strings.TrimLeft(l, " │┃"))
		if strings.HasPrefix(low, "↑/↓ navigate") || strings.HasPrefix(low, "keyboard: ↑/↓ navigate") {
			return true
		}
	}
	return false
}

// Trust and DontTrust are the answers to agy's folder-trust question:
// Enter on its first row, which the cursor starts on, and Esc, which
// closes agy as its "No, exit" does.
const (
	Trust     = "Trust"
	DontTrust = "Don't trust"
)

// AgyPromptDetail reads the human-facing detail (command, question, trust or
// plan prompt) of an Antigravity CLI prompt currently shown on g for state.
// Command approvals use agy's own "Approve Bash: <command>" phrasing so
// desktop chat cards and sidebar rows match OSC 7501 reports identically.
func AgyPromptDetail(state model.AgentState, g vt.Grid) string {
	switch state {
	case model.StateAwaitingInput:
		if AgyTrustAsked(g) {
			return "Trust this folder?"
		}
		if q := ReadQuestion(model.ProviderAntigravity, g); q != nil {
			return Summary(q.Prompt)
		}
		return ""
	case model.StatePlanReady:
		return "Approve plan"
	case model.StatePendingApproval:
		return agyApprovalDetail(g)
	default:
		return ""
	}
}

// agyApprovalDetail extracts the command or action from an agy permission
// prompt on g, formatted like agy's OSC 7501 message ("Approve Bash: <cmd>").
func agyApprovalDetail(g vt.Grid) string {
	rows := screenRows(g)
	reqRow := -1
	reqToRow := -1
	askRow := -1
	for y, l := range rows {
		low := strings.ToLower(strings.TrimSpace(l))
		switch {
		case strings.Contains(low, "requesting permission for:"):
			reqRow = y
		case strings.Contains(low, "requesting permission to "):
			reqToRow = y
		case low == "run this command?" || low == "allow access to this file?" || low == "allow access to this url?" || low == "allow calling this tool?":
			askRow = y
		}
	}
	if reqRow >= 0 {
		header := agySectionHeader(rows, reqRow)
		var parts []string
		isCmd := strings.EqualFold(header, "command")
		for y := reqRow + 1; y < len(rows); y++ {
			t := strings.TrimSpace(rows[y])
			if t == "" {
				continue
			}
			low := strings.ToLower(t)
			if low == "run this command?" {
				isCmd = true
				break
			}
			if strings.HasSuffix(t, "?") || optionLine.MatchString(rows[y]) ||
				strings.HasPrefix(low, "↑/↓ navigate") || strings.HasPrefix(low, "keyboard: ↑/↓ navigate") {
				break
			}
			parts = append(parts, t)
		}
		if target := strings.Join(parts, " "); target != "" {
			if isCmd {
				return Summary("Approve Bash: " + target)
			}
			if header != "" {
				return Summary("Approve " + header + ": " + target)
			}
			return Summary(target)
		}
	}
	if reqToRow >= 0 {
		return Summary(strings.TrimSpace(rows[reqToRow]))
	}
	if askRow >= 0 {
		return Summary(strings.TrimSpace(rows[askRow]))
	}
	return ""
}

// agySectionHeader returns the section title drawn just above the horizontal
// rule preceding row (such as "Command"), or "".
func agySectionHeader(rows []string, row int) string {
	for y := row - 1; y >= 0 && y >= row-4; y-- {
		t := strings.TrimSpace(rows[y])
		if t == "" {
			continue
		}
		if ruleOf(t, "─") {
			for k := y - 1; k >= 0 && k >= y-2; k-- {
				if h := strings.TrimSpace(rows[k]); h != "" {
					if len(h) <= 32 && !strings.Contains(h, "  ") {
						return h
					}
					return ""
				}
			}
			return ""
		}
	}
	return ""
}

// agyOffers are the answers agy's form on g takes: Allow always on a command
// approval, Approve plan on an artifact/plan review, Trust and Don't trust on
// its folder-trust question, and the single-select options on a question.
func agyOffers(a model.Activity, g vt.Grid) []Offer {
	switch a.State {
	case model.StatePendingApproval:
		if StateOf(model.ProviderAntigravity, g) == model.StatePendingApproval && AgyAlwaysOffered(g) {
			return []Offer{{Label: AllowAlways, Key: "2"}}
		}
	case model.StatePlanReady:
		if StateOf(model.ProviderAntigravity, g) == model.StatePlanReady {
			return []Offer{{Label: ApprovePlan, Key: "y"}}
		}
	case model.StateAwaitingInput:
		if AgyTrustAsked(g) && strings.Contains(ScreenText(g, screenLines), "> Yes, I trust this folder") {
			return []Offer{{Label: Trust, Key: "⏎"}, {Label: DontTrust, Key: "⎋"}}
		}
		if q := ReadQuestion(model.ProviderAntigravity, g); q != nil && !q.Multi && len(q.Options) > 0 {
			out := make([]Offer, len(q.Options))
			for i, o := range q.Options {
				out[i] = Offer{Label: o.Label, Key: strconv.Itoa(i + 1)}
			}
			return out
		}
	}
	return nil
}

// samePath reports whether a and b name one folder: both resolved through
// symlinks where they exist, which on Windows also expands a short name
// such as RUNNER~1, and cleaned; Windows compares them without case, and
// either kind of slash is a separator there.
func samePath(a, b string) bool {
	a, b = resolvePath(a), resolvePath(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// resolvePath is p cleaned, through its symlinks when it exists.
func resolvePath(p string) string {
	p = filepath.Clean(filepath.FromSlash(p))
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return p
}
