package daemon

import (
	"os"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/repoicon"
)

// favicon answers m with the repo root above m.Dir and as many of its
// icon files, best first, as fit in repoicon.Max bytes.
func favicon(m proto.Favicon) proto.FaviconResult {
	r := proto.FaviconResult{Dir: m.Dir, Root: repoicon.Root(m.Dir)}
	if r.Root == "" {
		return r
	}
	n := 0
	for _, f := range repoicon.Files(r.Root) {
		b, err := os.ReadFile(f)
		if err != nil || n+len(b) > repoicon.Max {
			continue
		}
		n += len(b)
		r.Files = append(r.Files, proto.FaviconFile{Name: f, Data: b})
	}
	return r
}
