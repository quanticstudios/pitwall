package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/model"
)

// runResult is the JSON object printed by `pitwall new --wait --json` and
// `pitwall split --wait --json`.
type runResult struct {
	Tab      string `json:"tab"`
	TabID    string `json:"tab_id"`
	Pane     string `json:"pane"`
	PaneID   string `json:"pane_id"`
	Outcome  string `json:"outcome,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Signal   bool   `json:"signal,omitempty"`
	Unknown  bool   `json:"unknown,omitempty"`
}

// waitPaneExit waits until paneID's process exits using control.NewWait("exit")
// (reusing the exact evaluator of `pitwall wait`). When remoteHost is non-empty,
// it watches that host through hostWait.
func waitPaneExit(watch *watcher, initial model.State, paneID, remoteHost string, timeout time.Duration, errOut io.Writer) (control.Outcome, int, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var timedOut atomic.Bool
	var cur atomic.Pointer[watcher]
	cur.Store(watch)
	if timeout > 0 {
		t := time.AfterFunc(timeout, func() {
			timedOut.Store(true)
			cancel()
			if w := cur.Load(); w != nil {
				w.Close()
			}
		})
		defer t.Stop()
	}

	var remote *hostWait
	state := initial
	if remoteHost != "" {
		remote = &hostWait{name: remoteHost}
		defer remote.close()
		var err error
		if state, err = remote.next(ctx, errOut); err != nil {
			if timedOut.Load() {
				return control.Outcome{}, waitTimeout, fmt.Errorf("timed out after %s", timeout)
			}
			return control.Outcome{}, waitError, err
		}
		watch = remote.watch
		cur.Store(watch)
	}

	wait := control.NewWait("exit")
	for {
		looked := false
		o := wait.Step(state, paneID, func() (int, bool) {
			looked = true
			return watch.exitOf(state, paneID)
		})
		if looked && timedOut.Load() {
			return control.Outcome{}, waitTimeout, fmt.Errorf("timed out after %s", timeout)
		}
		switch o.Reason {
		case control.Closed:
			return o, waitError, errors.New("pane closed before its command exited")
		case control.Exited:
			if o.Exit.Unknown {
				return o, waitExited, nil
			}
			if o.Exit.Signal {
				return o, waitError, nil
			}
			return o, o.Exit.Code, nil
		}
		var err error
		if remote != nil {
			state, err = remote.next(ctx, errOut)
			if err == nil {
				watch = remote.watch
				cur.Store(watch)
			}
		} else {
			state, err = watch.next()
		}
		if err != nil {
			if timedOut.Load() {
				return control.Outcome{}, waitTimeout, fmt.Errorf("timed out after %s", timeout)
			}
			return control.Outcome{}, waitError, err
		}
	}
}

func encodeRunResult(out io.Writer, res runResult, o control.Outcome) error {
	res.Outcome = o.Reason
	if o.Exit != nil {
		res.Unknown = o.Exit.Unknown
		res.Signal = o.Exit.Signal
		if !o.Exit.Unknown {
			code := o.Exit.Code
			res.ExitCode = &code
		}
	}
	return json.NewEncoder(out).Encode(res)
}
