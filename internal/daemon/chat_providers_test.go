package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session/acp"
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
	if got := configuredProviders(); len(got) != 0 {
		t.Fatalf("without the opt-in: %v", got)
	}
	t.Setenv("PITWALL_FAKE_PROVIDERS", "1")
	got := configuredProviders()
	if got[model.ProviderClaude] == nil || got[model.ProviderCodex] == nil {
		t.Fatalf("with PITWALL_FAKE_PROVIDERS=1: %v", got)
	}
}
