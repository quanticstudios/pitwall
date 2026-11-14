package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

// skillshareCalls owns one connection's work and audit clones. A canceled
// waiter may never receive its discovery reply, so cleanup belongs here.
type skillshareCalls struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	active map[uint64]context.CancelFunc
	clones map[string]*skillshareClone
}

type skillshareClone struct {
	id    uint64
	users int
	drop  bool
}

func (s *skillshareCalls) release(dir string) {
	if clone := s.clones[dir]; clone != nil && clone.drop && clone.users == 0 {
		delete(s.clones, dir)
		skillshare.Drop(dir)
	}
}

func (s *skillshareCalls) request(ctx context.Context, d *Daemon, r proto.Skillshare, send func(any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Cancel {
		if cancel := s.active[r.ID]; cancel != nil {
			cancel()
		}
		for dir, clone := range s.clones {
			if clone.id == r.ID {
				clone.drop = true
				s.release(dir)
			}
		}
		return
	}
	if s.active == nil {
		s.active = map[uint64]context.CancelFunc{}
		s.clones = map[string]*skillshareClone{}
	}
	if s.active[r.ID] != nil {
		return // a repeated ID must not replace the original call's cancel
	}
	for _, clone := range s.clones {
		if clone.id == r.ID {
			return
		}
	}
	if r.Call.Op == skillshare.OpDrop {
		if clone := s.clones[r.Call.Clone]; clone != nil {
			clone.drop = true
			s.release(r.Call.Clone)
		}
		send(proto.SkillshareResult{ID: r.ID, Done: true})
		return
	}
	var clone *skillshareClone
	if r.Call.Op == skillshare.OpAudit {
		clone = s.clones[r.Call.Clone]
		if clone == nil || clone.drop {
			send(proto.SkillshareResult{ID: r.ID, Done: true, Reply: skillshare.Failed("The install plan is no longer available. Start the install again.")})
			return
		}
		clone.users++
	}
	ctx, cancel := context.WithCancel(ctx)
	s.active[r.ID] = cancel
	s.wg.Go(func() {
		defer cancel()
		d.skillshare(ctx, r, func(msg any) {
			m := msg.(proto.SkillshareResult)
			if m.Done {
				s.mu.Lock()
				delete(s.active, r.ID)
				if clone != nil {
					clone.users--
					s.release(r.Call.Clone)
				}
				if err := ctx.Err(); err != nil {
					skillshare.Drop(m.Reply.Plan.Dir)
					m.Reply = skillshare.Failed(err.Error())
				} else if dir := m.Reply.Plan.Dir; dir != "" {
					s.clones[dir] = &skillshareClone{id: r.ID}
				}
				s.mu.Unlock()
			}
			send(m)
		})
	})
}

func (s *skillshareCalls) close() {
	s.mu.Lock()
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	for dir := range s.clones {
		skillshare.Drop(dir)
	}
}

// skillshare does r's call on this machine and sends its steps and then
// its reply, which skillshare.Client.Serve masks, to the asking client.
func (d *Daemon) skillshare(ctx context.Context, r proto.Skillshare, send func(any)) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	reply := skillshare.Failed("This pitwall daemon has no skillshare.")
	if cl := d.o.Skillshare; cl.Run != nil {
		reply = cl.Serve(ctx, r.Call, func(step string) { send(proto.SkillshareResult{ID: r.ID, Step: step}) })
	}
	send(proto.SkillshareResult{ID: r.ID, Done: true, Reply: reply})
}
