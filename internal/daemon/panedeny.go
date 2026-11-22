package daemon

import (
	"context"
	"errors"
	"strings"
	"time"

	"gioui.org/io/key"
	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// pressFeedback denies pane's permission prompt or plan with reason through
// Claude Code's own field for it (agent.Feedback): the cursor onto No, Tab
// when that opens the field, the reason, Enter. Claude reads the reason and
// carries on with the turn, as with "No, and tell Claude what to do
// differently" typed in the terminal.
func (d *Daemon) pressFeedback(pane string, at int64, reason string) error {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	p, a, _, err := d.waiting(pane, at)
	if err != nil {
		return err
	}
	// why: the field is one line; a newline in it would send the rest as Enter.
	reason = strings.Join(strings.Fields(remote.Clean(reason)), " ")
	g := p.Snapshot()
	if s := agent.StateOf(a.Provider, g); a.Provider != model.ProviderClaude || s != model.StatePendingApproval && s != model.StatePlanReady {
		return errors.New("the pane shows no prompt to deny")
	}
	target, cursor, tab, ok := agent.Feedback(g)
	if !ok {
		return errors.New("the pane's prompt takes no reason; answer it in its tab")
	}
	send := func(data []byte) error { return d.handle(context.Background(), proto.Input{Pane: pane, Data: data}) }
	d.noteUserKey(pane, nil)
	dir := key.NameDownArrow
	if target < cursor {
		dir = key.NameUpArrow
	}
	for i := cursor; i != target; i += min(1, max(-1, target-i)) {
		if err := send(press(dir, p.Modes())); err != nil {
			return err
		}
	}
	if tab {
		if err := send(press(key.NameTab, p.Modes())); err != nil {
			return err
		}
	}
	// The field draws after the cursor lands on it; wait for that render.
	time.Sleep(replyDelay.get())
	if err := send(input.Paste(reason, p.Modes())); err != nil {
		return err
	}
	time.Sleep(replyDelay.get())
	return send(press(key.NameReturn, p.Modes()))
}
