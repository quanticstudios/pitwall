package daemon

import (
	"context"
	"errors"
	"time"

	"gioui.org/io/key"
	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// phoneQuestion checks the exact page before sending navigation or text.
// option -1 submits a multi-select page; -2 answers Other.
func (d *Daemon) phoneQuestion(pane, id string, option int, text string) error {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	p := d.panes[pane]
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if p == nil || d.paneExited(pane) {
		return errors.New("the agent exited")
	}
	q := agent.ReadQuestion(prov, p.Snapshot())
	if q == nil || q.ID != id {
		return errors.New("the question changed; refresh and answer again")
	}
	if q.Draft {
		return errors.New("there are notes in the terminal; send or clear them first")
	}
	target := option
	switch option {
	case -1:
		if !q.Submit {
			return errors.New("this question has no Submit")
		}
		target = len(q.Options) + 1
	case -2:
		if !q.Other || remote.Clean(text) == "" {
			return errors.New("type an answer for Other")
		}
		target = q.OtherIndex
	default:
		if option < 0 || option >= len(q.Options) {
			return errors.New("invalid option")
		}
	}
	send := func(data []byte) error { return d.handle(context.Background(), proto.Input{Pane: pane, Data: data}) }
	d.noteUserKey(pane, nil)
	if q.Notes {
		if err := send(press(key.NameTab, p.Modes())); err != nil {
			return err
		}
		time.Sleep(replyDelay.get())
	}
	direction := key.NameDownArrow
	if target < q.Cursor {
		direction = key.NameUpArrow
	}
	for i := q.Cursor; i != target; {
		if err := send(press(direction, p.Modes())); err != nil {
			return err
		}
		if target < i {
			i--
		} else {
			i++
		}
	}
	if option == -2 {
		if prov == model.ProviderCodex {
			if err := send(press(key.NameTab, p.Modes())); err != nil {
				return err
			}
		}
		// Moving onto Other focuses its field; wait for that render before pasting.
		time.Sleep(replyDelay.get())
		if err := send(input.Paste(remote.Clean(text), p.Modes())); err != nil {
			return err
		}
		time.Sleep(replyDelay.get())
	}
	k := key.NameReturn
	if q.Multi && option >= 0 {
		k = key.NameSpace
	}
	return send(press(k, p.Modes()))
}
