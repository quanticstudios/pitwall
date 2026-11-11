package mobile

import (
	"context"
	"errors"
	"time"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// iconFetch is a repo icon asked of a machine: its bytes once done, none
// for a machine that serves no such icon.
type iconFetch struct {
	data       []byte
	svg        bool
	done, busy bool
	retry      time.Time // after a failed fetch, when to ask again
}

// iconRetry is how long a fetch that failed, the machine down, waits.
const iconRetry = time.Minute

// repoIcon is the sidebar's RepoIcons: a row's repo is the folder
// phone.State gave it, and its icon the one its hash names, fetched once
// from its machine. A repo without one, or a machine that will not serve
// it, gets the sidebar's letter tile; false while the fetch runs.
func (a *App) repoIcon(_, dir string) (proto.FaviconResult, bool) {
	machine, hash, ok := phone.ParseIconDir(dir)
	if !ok {
		return proto.FaviconResult{Dir: dir}, true
	}
	r := proto.FaviconResult{Dir: dir, Root: dir}
	if hash == remote.NoIcon {
		return r, true
	}
	if a.icons == nil {
		a.icons = map[string]*iconFetch{}
	}
	k := machine + "\x00" + hash
	f := a.icons[k]
	if f == nil {
		f = &iconFetch{}
		a.icons[k] = f
	}
	switch {
	case f.done:
		if f.data != nil {
			name := "icon"
			if f.svg {
				name = "icon.svg" // why: the sidebar tells an SVG by its name
			}
			r.Files = []proto.FaviconFile{{Name: name, Data: f.data}}
		}
		return r, true
	case f.busy || time.Now().Before(f.retry):
		return r, false
	}
	f.busy = true
	var data []byte
	var svg bool
	a.do(machine, func(ctx context.Context, c *phone.Client) (err error) {
		data, svg, err = c.Icon(ctx, hash)
		return err
	}, func(err error) {
		f.busy = false
		switch {
		case err == nil:
			f.data, f.svg, f.done = data, svg, true
		case errors.Is(err, phone.ErrNoIcon):
			f.done = true
		default:
			f.retry = time.Now().Add(iconRetry)
		}
	})
	return r, false
}
