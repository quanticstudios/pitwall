package daemon

import (
	"log"
	"os"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/acp"
	"github.com/quanticstudios/pitwall/internal/session/fake"
)

// configuredProviders reads once at daemon startup, keeping a provider id's
// executable stable for the lifetime of its chats. No agent is spawned here.
// PITWALL_FAKE_PROVIDERS=1 adds the scripted Claude and Codex for aide's e2e.
func configuredProviders() map[model.Provider]session.Provider {
	s, problems := config.Load()
	for _, p := range problems {
		log.Printf("chat providers: %s", p.Msg)
	}
	return fake.ProvidersFromEnv(acpProviders(s.ACPAgents), nil)
}

// gatesFromEnv are aide's Jev gates; with PITWALL_FAKE_PROVIDERS=1 their
// Jev is the fake that answers 0.8 to everything, for aide's e2e.
func gatesFromEnv() *decide.Gates {
	if os.Getenv("PITWALL_FAKE_PROVIDERS") == "1" {
		return decide.FakeGates()
	}
	return &decide.Gates{}
}

func acpProviders(agents []config.ACPAgent) map[model.Provider]session.Provider {
	out := map[model.Provider]session.Provider{}
	for _, a := range agents {
		out[model.Provider("acp:"+a.Name)] = acp.Provider{Command: a.Command, Args: slices.Clone(a.Args), Env: slices.Clone(a.Env)}
	}
	return out
}

func (d *Daemon) chatProviders() proto.ChatProviderList {
	out := proto.ChatProviderList{Providers: []proto.ChatProvider{}}
	for id := range d.o.Providers {
		out.Providers = append(out.Providers, proto.ChatProvider{ID: id, Name: strings.TrimPrefix(string(id), "acp:")})
	}
	slices.SortFunc(out.Providers, func(a, b proto.ChatProvider) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out
}
