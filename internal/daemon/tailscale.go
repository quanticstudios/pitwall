package daemon

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/tsnet"
	"github.com/quanticstudios/pitwall/internal/web"
)

type tailscaleNode interface {
	UpdatePolicy(allowUsers, allowTags []string)
	Stop(logout bool) error
}

type tsnetStartConfig struct {
	Config       tsnet.Config
	BuildHandler func(authorize func(*http.Request) (remote.Device, int, string), deviceLive func(remote.Device) bool) http.Handler
}

func defaultTailscaleStart(c tsnetStartConfig) (tailscaleNode, error) {
	return tsnet.Start(c.Config, c.BuildHandler)
}

// tailscaleLoop manages the opt-in built-in Tailscale (tsnet) listener for the
// phone page. Turning [remote.tailscale] off logs the node out cleanly and
// removes its state when requested; shutting the daemon down closes the node
// without logging out so restarts reconnect seamlessly.
func (d *Daemon) tailscaleLoop(ctx context.Context) {
	if d.o.Remote == nil || d.o.RemoteDir == "" {
		return
	}
	startFn := d.o.TailscaleStart
	if startFn == nil {
		startFn = defaultTailscaleStart
	}
	interval := d.o.TailscaleCheck
	if interval <= 0 {
		interval = remoteCheck
	}

	var (
		node        tailscaleNode
		ws          *web.Server
		releasePush func()
		cur         config.RemoteTailscaleSettings
		curBundle   string
	)

	stopNode := func(logout bool) {
		if releasePush != nil {
			releasePush()
			releasePush = nil
		}
		if ws != nil {
			ws.Close()
			ws = nil
		}
		if node != nil {
			if err := node.Stop(logout); err != nil {
				log.Printf("remote: tailscale: stop (logout=%v): %v", logout, err)
			}
			node = nil
		}
	}

	defer func() {
		// Daemon exit / handoff / reboot: preserve tailnet registration.
		stopNode(false)
	}()

	startNode := func(s config.RemoteTailscaleSettings, bundle string) {
		cfg := tsnet.Config{
			Dir:        remote.TailscaleDir(d.o.RemoteDir),
			Hostname:   s.Hostname,
			AuthKey:    s.AuthKey,
			AllowUsers: s.AllowUsers,
			AllowTags:  s.AllowTags,
			OnStatus: func(st remote.TailscaleStatus) {
				_ = remote.WriteTailscaleStatus(d.o.RemoteDir, st)
			},
		}
		var newWS *web.Server
		build := func(authorize func(*http.Request) (remote.Device, int, string), deviceLive func(remote.Device) bool) http.Handler {
			rs := d.remoteServer()
			rs.Authorize = authorize
			rs.DeviceLive = func(dev remote.Device) bool {
				if dev.Tailscale {
					return deviceLive(dev)
				}
				return remote.Paired(d.o.RemoteDir, dev.ID)
			}
			if bundle != "" {
				rs.Web = &web.Client{
					Dir: bundle,
					Live: func(id string) bool {
						if strings.HasPrefix(id, "ts-") {
							return deviceLive(remote.Device{ID: id, Tailscale: true})
						}
						return remote.Paired(d.o.RemoteDir, id)
					},
				}
				newWS = &web.Server{Version: Build, Client: rs.Web}
				rs.WS = newWS.WebSocket()
			}
			return rs.Handler()
		}
		n, err := startFn(tsnetStartConfig{Config: cfg, BuildHandler: build})
		if err != nil {
			if ok, _ := remoteFails.Allow("tailscale", time.Now()); ok {
				log.Printf("remote: tailscale: %q", err)
			}
			if newWS != nil {
				newWS.Close()
			}
			return
		}
		node = n
		ws = newWS
		releasePush = d.acquirePush()
		cur = s
		curBundle = bundle
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s := d.o.Remote().Tailscale
		bundle := ""
		if d.o.Web != nil {
			bundle = d.o.Web().Bundle
		}

		if !s.Enabled {
			if node != nil {
				stopNode(true)
			}
			if remote.ConsumeTailscalePurge(d.o.RemoteDir) {
				if err := remote.RemoveTailscaleState(d.o.RemoteDir); err != nil {
					log.Printf("remote: tailscale: remove state: %v", err)
				}
			}
			if cur.Enabled {
				_ = remote.WriteTailscaleStatus(d.o.RemoteDir, remote.TailscaleStatus{
					Enabled:  false,
					State:    remote.TailscaleOff,
					Hostname: s.Hostname,
					PID:      os.Getpid(),
				})
				cur = s
				curBundle = ""
			}
		} else {
			switch {
			case node == nil:
				startNode(s, bundle)
			case s.Hostname != cur.Hostname || bundle != curBundle:
				// Hostname or web bundle changed: restart node without logging out.
				stopNode(false)
				startNode(s, bundle)
			case !s.Equal(cur):
				// Only allow_users / allow_tags (or auth_key) changed: update policy live.
				node.UpdatePolicy(s.AllowUsers, s.AllowTags)
				cur = s
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
