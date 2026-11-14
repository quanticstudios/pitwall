package daemon

import (
	"context"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

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
