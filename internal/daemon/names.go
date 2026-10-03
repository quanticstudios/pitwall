package daemon

import (
	"math/rand/v2"

	"github.com/quanticstudios/pitwall/internal/model"
)

// freshName is an adjective-noun name no session has. Callers hold d.mu.
func (d *Daemon) freshName() string {
	for n := 0; ; n++ {
		if name := model.SessionName(rand.IntN, n); !d.nameTaken(name, "") {
			return name
		}
	}
}
