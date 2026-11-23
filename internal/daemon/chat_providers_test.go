package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session/acp"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/session/codex"
)

func TestConfiguredACPProviders(t *testing.T) {
	// Load through the real TOML resolver, register once, then discover over
	// the same socket every client uses. Discovery must not start the command.
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`[[agents.acp]]
name = "grok"
command = "missing-acp-command"
args = ["--acp"]
env = ["TOKEN=synthetic-secret"]
[[agents.acp]]
name = "antigravity"
command = "missing-agent"
`), 0600); err != nil {
		t.Fatal(err)
	}
	settings, problems := config.LoadFile(path)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	providers := acpProviders(settings.ACPAgents)
	p := providers["acp:grok"].(acp.Provider)
	if p.Command != "missing-acp-command" || !reflect.DeepEqual(p.Args, []string{"--acp"}) || !reflect.DeepEqual(p.Env, []string{"TOKEN=synthetic-secret"}) {
		t.Fatalf("provider: %+v", p)
	}
	settings.ACPAgents[0].Env[0] = "CHANGED=1"
	if p.Env[0] != "TOKEN=synthetic-secret" {
		t.Fatal("registry borrowed config slices")
	}
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := runWith(t, f, func(o *Options) { o.Providers = providers })
	defer stop()
	c := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "cli"})
	got := chatCall[proto.ChatProviderList](c, proto.ChatProviders{})
	want := []proto.ChatProvider{{ID: "acp:antigravity", Name: "antigravity"}, {ID: "acp:grok", Name: "grok"}}
	if !reflect.DeepEqual(got.Providers, want) {
		t.Fatalf("providers: %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "synthetic-secret") || strings.Contains(string(b), "missing-acp-command") {
		t.Fatal("provider discovery leaked launch configuration")
	}
	// The id in discovery selects that registered driver rather than a CLI
	// fallback. Missing executable gives a provider error, not unsupported.
	r := chatCall[proto.ChatResult](c, proto.ChatNew{Provider: model.Provider("acp:grok"), Dir: t.TempDir()})
	if r.Code != "provider" {
		t.Fatalf("configured provider not selected: %+v", r)
	}
}

func TestConfiguredFakeProviders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PITWALL_FAKE_PROVIDERS", "")
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if got := configuredProviders(); len(got) != 1 || got[model.ProviderClaude] != (claude.Provider{}) {
		t.Fatalf("without the opt-in, want the real Claude driver alone: %v", got)
	}
	// Antigravity's chats run agy acp, found on PATH.
	agy := filepath.Join(bin, "agy")
	if runtime.GOOS == "windows" {
		agy += ".exe" // LookPath finds only a name with an executable extension
	}
	if err := os.WriteFile(agy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := configuredProviders()[model.ProviderAntigravity]; !reflect.DeepEqual(got, agyACP{acp.Provider{Command: agy, Args: []string{"acp"}}}) {
		t.Fatalf("agy on PATH: %#v", got)
	}
	// Codex's chats run codex app-server, found on PATH or named by
	// PITWALL_CODEX_COMMAND.
	if got := configuredProviders()[model.ProviderCodex]; got != nil {
		t.Fatalf("no codex on PATH: %#v", got)
	}
	t.Setenv("PITWALL_CODEX_COMMAND", agy)
	if got := configuredProviders()[model.ProviderCodex]; got != (codex.Provider{}) {
		t.Fatalf("PITWALL_CODEX_COMMAND: %#v", got)
	}
	t.Setenv("PITWALL_FAKE_PROVIDERS", "1")
	got := configuredProviders()
	if got[model.ProviderClaude] == nil || got[model.ProviderCodex] == nil {
		t.Fatalf("with PITWALL_FAKE_PROVIDERS=1: %v", got)
	}
}
