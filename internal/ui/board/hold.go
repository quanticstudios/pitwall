package board

import (
	"slices"
	"time"
)

// Arming is how long a card that just arrived in Needs you, or whose ask
// just changed, ignores clicks on its answers.
const Arming = 650 * time.Millisecond

// Tracker keeps cards from moving under the user's hand: a card under the
// pointer or keyboard focus stays where it was, and an ask that just
// arrived takes no clicks for Arming.
type Tracker struct {
	last   map[string]placed
	arrive map[string]arrival
}

type placed struct {
	card  Card
	index int
}

// arrival is when a card's current ask, named by its activity's time,
// arrived in Needs you.
type arrival struct {
	ask time.Time
	at  time.Time
}

// Hold puts each card in held, the ids under the pointer or focus, back
// where it was last time if it has moved: the card as it was, with Held
// set and Now its new kind. A held card whose tab is gone goes. Cards not
// held take their new places. Call it once per frame after Derive.
func (t *Tracker) Hold(b *Board, held map[string]bool, now time.Time) {
	next := map[string]placed{}
	for id := range held {
		old, ok := t.last[id]
		cur, live := b.All[id]
		if !ok || !live {
			continue
		}
		col := old.card.Column()
		at := slices.IndexFunc(b.Cols[col], func(c Card) bool { return c.ID == id })
		if at >= 0 && !b.Cols[col][at].Held && b.Cols[col][at].Kind == old.card.Kind {
			continue // still there, as it was
		}
		for c := range b.Cols {
			b.Cols[c] = slices.DeleteFunc(b.Cols[c], func(c Card) bool { return c.ID == id })
		}
		keep := old.card
		keep.Held, keep.Now = true, cur.Kind
		i := min(old.index, len(b.Cols[col]))
		b.Cols[col] = slices.Insert(b.Cols[col], i, keep)
	}
	if t.arrive == nil {
		t.arrive = map[string]arrival{}
	}
	seen := map[string]bool{}
	for col := range b.Cols {
		for i, c := range b.Cols[col] {
			next[c.ID] = placed{c, i}
			if Column(col) != NeedsYou || c.Held {
				continue
			}
			seen[c.ID] = true
			if a, ok := t.arrive[c.ID]; !ok || !a.ask.Equal(c.Act.UpdatedAt) {
				t.arrive[c.ID] = arrival{c.Act.UpdatedAt, now}
			}
		}
	}
	for id := range t.arrive {
		if !seen[id] {
			delete(t.arrive, id)
		}
	}
	t.last = next
}

// Armed reports whether card id's answers take clicks at now: it has been
// in Needs you, with the same ask, for Arming.
func (t *Tracker) Armed(id string, now time.Time) bool {
	a, ok := t.arrive[id]
	return ok && now.Sub(a.at) >= Arming
}

// ArmsAt is when card id's answers start to take clicks, zero when they
// do already or it has none.
func (t *Tracker) ArmsAt(id string, now time.Time) time.Time {
	a, ok := t.arrive[id]
	if !ok || now.Sub(a.at) >= Arming {
		return time.Time{}
	}
	return a.at.Add(Arming)
}
