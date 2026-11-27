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
// option -1 submits a multi-select page; -2 answers Other; -3 goes Back (←);
// -4 goes Next (→).
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
	send := func(data []byte) error { return d.handle(context.Background(), proto.Input{Pane: pane, Data: data}) }
	d.noteUserKey(pane, nil)
	if option == -3 || option == -4 {
		if option == -3 && !q.CanBack {
			return errors.New("this question has no previous step")
		}
		if option == -4 && !q.CanNext {
			return errors.New("this question has no next step")
		}
		if q.Notes {
			if err := send(press(key.NameTab, p.Modes())); err != nil {
				return err
			}
			time.Sleep(replyDelay.get())
		}
		k := key.NameLeftArrow
		if option == -4 {
			k = key.NameRightArrow
		}
		return send(press(k, p.Modes()))
	}
	target := option
	switch option {
	case -1:
		if !q.Submit {
			return errors.New("this question has no Submit")
		}
		if prov == model.ProviderAntigravity {
			if q.Cursor == q.OtherIndex && len(q.Options) > 0 {
				target = 0
			} else {
				target = q.Cursor
			}
		} else if q.SubmitIndex >= 0 {
			target = q.SubmitIndex
		} else {
			target = len(q.Options) + 1
		}
	case -2:
		if !q.Other || remote.Clean(text) == "" {
			return errors.New("type an answer for Other")
		}
		if q.Chat {
			return d.answerByChat(pane, q, text)
		}
		target = q.OtherIndex
	default:
		if option < 0 || option >= len(q.Options) {
			return errors.New("invalid option")
		}
	}
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
		switch prov {
		case model.ProviderCodex:
			if err := send(press(key.NameTab, p.Modes())); err != nil {
				return err
			}
		case model.ProviderAntigravity:
			if err := send(press(key.NameReturn, p.Modes())); err != nil {
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
