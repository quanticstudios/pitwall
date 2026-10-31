package config

import "fmt"

// Web is [web]: the listener web clients such as aide use (docs/web-protocol.md).
type Web struct {
	Enabled *bool    `toml:"enabled" doc:"Serve the web protocol on 127.0.0.1 for local web clients such as aide. They read the address and token from web.json in the state folder. Off by default."`
	Port    *float64 `toml:"port" min:"0" max:"65535" doc:"Port to serve on, 1 to 65535. 0 lets the system pick a free one each time the service starts."`
}

// WebSettings is [web] with every value filled in.
type WebSettings struct {
	Enabled bool
	Port    int // 0 picks one
}

func defaultWeb() Web {
	off, pick := false, 0.0
	return Web{Enabled: &off, Port: &pick}
}

// resolveWeb fills in defaults. A port out of range keeps the listener off.
func resolveWeb(c Web) (WebSettings, []issue) {
	w := WebSettings{Enabled: c.Enabled != nil && *c.Enabled}
	if p := c.Port; p != nil {
		if *p < 0 || *p > 65535 || *p != float64(int(*p)) {
			w.Enabled = false
			return w, []issue{{"web.port", fmt.Sprintf("%g is not a whole number from 0 to 65535; the web listener stays off", *p)}}
		}
		w.Port = int(*p)
	}
	return w, nil
}

// LoadWeb reads only [web] from the config at path, for the daemon.
func LoadWeb(path string) WebSettings {
	s, _ := LoadFile(path)
	return s.Web
}
