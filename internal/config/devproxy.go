package config

import "fmt"

// DevProxy is [dev_proxy], the local worktree HTTP proxy.
type DevProxy struct {
	Enabled *bool    `toml:"enabled" doc:"Serve named worktree dev servers on 127.0.0.1. Set false to keep direct port URLs. Restart the daemon and panes after changing these settings."`
	Port    *float64 `toml:"port" min:"1024" max:"65535" doc:"Port for folder.repo.localhost URLs. Default 7778, beside the phone page's 7777. Never listens outside 127.0.0.1."`
}

// DevProxySettings is [dev_proxy] with defaults filled in.
type DevProxySettings struct {
	Enabled bool
	Port    int
}

func defaultDevProxy() DevProxy {
	on, port := true, 7778.0
	return DevProxy{Enabled: &on, Port: &port}
}

func resolveDevProxy(c DevProxy) (DevProxySettings, []issue) {
	s := DevProxySettings{Enabled: c.Enabled == nil || *c.Enabled, Port: 7778}
	if p := c.Port; p != nil {
		if *p < 1024 || *p > 65535 || *p != float64(int(*p)) {
			s.Enabled = false
			return s, []issue{{"dev_proxy.port", fmt.Sprintf("%g is not a whole number from 1024 to 65535; the dev proxy stays off", *p)}}
		}
		s.Port = int(*p)
	}
	return s, nil
}

// URLPort is the configured port, or zero when disabled.
func (s DevProxySettings) URLPort() int {
	if s.Enabled {
		return s.Port
	}
	return 0
}
