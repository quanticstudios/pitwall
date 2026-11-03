package daemon

import (
	"fmt"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/form"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Forms from `pitwall form` live as long as the connection that opened
// one: the daemon sends that connection the user's answer and drops the
// form when it goes. A form that asks something stands in for its pane's
// activity as a question, so the tab's row and the inbox show that it
// waits on the user.

// openForm is proto.OpenForm from client c.
func (d *Daemon) openForm(c *client, m proto.OpenForm) error {
	f, err := form.Parse([]byte(m.Spec))
	if err != nil {
		return fmt.Errorf("form %s: %w", m.ID, err)
	}
	if m.ID == "" {
		return fmt.Errorf("a form needs an id")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.st.Forms, func(f model.Form) bool { return f.ID == m.ID })
	if m.Update {
		if i < 0 {
			return fmt.Errorf("no form %s is open; show it first with pitwall form --id %s", m.ID, m.ID)
		}
		cur := &d.st.Forms[i]
		if f.Waits() && !cur.Waits {
			cur.OpenedAt = time.Now() // a dashboard that starts asking is a new question
		}
		cur.Spec, cur.Title, cur.Waits = m.Spec, f.Title, f.Waits()
		d.changed()
		return nil
	}
	if i >= 0 {
		return fmt.Errorf("form %s is open already; update it with pitwall form --id %s --update", m.ID, m.ID)
	}
	pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane })
	if pi < 0 {
		return fmt.Errorf("no pane %s", m.Pane)
	}
	ws := d.st.Panes[pi].WorkspaceID
	nf := model.Form{ID: m.ID, WorkspaceID: ws, PaneID: m.Pane, Spec: m.Spec, Title: f.Title, Waits: f.Waits(), OpenedAt: time.Now()}
	d.st.Forms = append(d.st.Forms, nf)
	if d.formOwner == nil {
		d.formOwner = map[string]*client{}
	}
	d.formOwner[m.ID] = c
	if w := d.workspace(ws); w != nil && nf.Waits {
		w.UpdatedAt = nf.OpenedAt
		// This new question wakes its family even when clock timestamps tie.
		workers := d.workers()
		for i := range d.st.Workspaces {
			w := &d.st.Workspaces[i]
			if w.ID == ws || workers[w.ID][ws] {
				w.Settled, w.SnoozedAt, w.SnoozedUntil = time.Time{}, time.Time{}, time.Time{}
			}
		}
	}
	d.changed()
	return nil
}

// answerForm is proto.FormAnswer: the first answer goes to the form's
// owner and ends the form; later ones find it gone.
func (d *Daemon) answerForm(m proto.FormAnswer) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.st.Forms, func(f model.Form) bool { return f.ID == m.ID })
	if i < 0 {
		return fmt.Errorf("no form %s is open", m.ID)
	}
	d.st.Forms = slices.Delete(d.st.Forms, i, i+1)
	if c := d.formOwner[m.ID]; c != nil {
		c.queue(m)
	}
	delete(d.formOwner, m.ID)
	d.changed()
	return nil
}

// dropForms ends the forms client c opened, as it disconnects.
func (d *Daemon) dropForms(c *client) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.st.Forms)
	d.st.Forms = slices.DeleteFunc(d.st.Forms, func(f model.Form) bool { return d.formOwner[f.ID] == c })
	for id, o := range d.formOwner {
		if o == c {
			delete(d.formOwner, id)
		}
	}
	if len(d.st.Forms) != n && !d.closing {
		d.changed()
	}
}

// dropOrphanForms ends each form whose pane is gone, telling its owner
// Closed: nobody can answer it any more. Callers hold d.mu.
func (d *Daemon) dropOrphanForms() {
	d.st.Forms = slices.DeleteFunc(d.st.Forms, func(f model.Form) bool {
		if slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == f.PaneID }) {
			return false
		}
		if c := d.formOwner[f.ID]; c != nil {
			c.queue(proto.FormAnswer{ID: f.ID, Closed: true})
		}
		delete(d.formOwner, f.ID)
		return true
	})
}

// formActivity is the question activity form f puts on its pane.
func formActivity(f model.Form, prov model.Provider) model.Activity {
	detail := f.Title
	if detail == "" {
		detail = "A form waits for you"
	}
	return model.Activity{PaneID: f.PaneID, WorkspaceID: f.WorkspaceID, Provider: prov,
		State: model.StateAwaitingInput, Detail: detail, UpdatedAt: f.OpenedAt}
}

// formActivities is acts with each pane that has a form waiting on the
// user showing that form as its question. Callers hold d.mu.
func (d *Daemon) formActivities(acts []model.Activity) []model.Activity {
	for _, f := range d.st.Forms {
		if !f.Waits {
			continue
		}
		pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == f.PaneID })
		if pi < 0 {
			continue
		}
		prov := d.st.Panes[pi].Provider
		if prov == "" {
			prov = model.ProviderTerminal
		}
		a := formActivity(f, prov)
		if i := slices.IndexFunc(acts, func(x model.Activity) bool { return x.PaneID == f.PaneID }); i >= 0 {
			a.SessionID = acts[i].SessionID
			acts[i] = a
		} else {
			acts = append(acts, a)
		}
	}
	return acts
}
