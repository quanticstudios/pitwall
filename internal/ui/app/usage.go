package app

import (
	"context"
	"sync"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
)

// usageWatch reads the token use of agent panes for the tabs' hover
// cards. A pane's session file is followed from the first time its tab's
// card asks until the pane goes or names another file, so tabs nobody
// hovers cost nothing.
type usageWatch struct {
	mu    sync.Mutex
	panes map[string]*paneUsage // by pane id
}

type paneUsage struct {
	key   string // provider and session file
	stop  context.CancelFunc
	usage *flow.Usage // nil until the first read
}

// of is the usage of tab ws's agent panes added up, nil while none has
// been read. It starts following the panes it does not follow yet;
// invalidate is called when one's usage lands.
func (w *usageWatch) of(st *model.State, ws string, invalidate func()) *flow.Usage {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.panes == nil {
		w.panes = map[string]*paneUsage{}
	}
	var sum *flow.Usage
	for _, p := range st.Panes {
		if p.WorkspaceID != ws || p.Provider == "" || p.Transcript == "" {
			continue
		}
		key := string(p.Provider) + "\x00" + p.Transcript
		pu := w.panes[p.ID]
		if pu == nil || pu.key != key {
			if pu != nil {
				pu.stop()
			}
			ctx, cancel := context.WithCancel(context.Background())
			pu = &paneUsage{key: key, stop: cancel}
			w.panes[p.ID] = pu
			go watchFeed(ctx, p.Provider, p.Transcript, func(f flow.Feed) {
				w.mu.Lock()
				if w.panes[p.ID] == pu {
					pu.usage = &f.Usage
				}
				w.mu.Unlock()
				invalidate()
			})
		}
		if pu.usage != nil {
			if sum == nil {
				sum = &flow.Usage{}
			}
			sum.Add(*pu.usage)
		}
	}
	return sum
}

// prune stops following the panes st no longer has; a nil st stops all.
func (w *usageWatch) prune(st *model.State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, pu := range w.panes {
		if st == nil || findPane(st, id) == nil {
			pu.stop()
			delete(w.panes, id)
		}
	}
}
