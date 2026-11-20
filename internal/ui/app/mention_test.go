package app

import (
	"reflect"
	"testing"
)

// The @ mention list: every file's folders join it; no query lists the
// top folder, folders first; a folder's slash lists its own entries; a
// query ranks fuzzily, a name match first.
func TestRankMentions(t *testing.T) {
	paths := withFolders([]string{"main.go", "internal/ui/app/app.go", "internal/ui/chatin/queue.go", "README.md", "approve/x.go"})
	want := []string{"README.md", "approve/", "approve/x.go", "internal/", "internal/ui/", "internal/ui/app/", "internal/ui/app/app.go",
		"internal/ui/chatin/", "internal/ui/chatin/queue.go", "main.go"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("withFolders: %q", paths)
	}
	for q, want := range map[string][]string{
		"":             {"approve/", "internal/", "README.md", "main.go"},
		"internal/ui/": {"internal/ui/app/", "internal/ui/chatin/"},
		"nothing/":     nil,
	} {
		if got := rankMentions(paths, q); !reflect.DeepEqual(got, want) {
			t.Errorf("rankMentions(%q) = %q, want %q", q, got, want)
		}
	}
	if got := rankMentions(paths, "queue"); len(got) == 0 || got[0] != "internal/ui/chatin/queue.go" {
		t.Errorf("queue: %q", got)
	}
	if got := rankMentions(paths, "app"); len(got) < 2 || got[0] != "internal/ui/app/" && got[0] != "internal/ui/app/app.go" {
		t.Errorf("app: %q, want the app folder or app.go before approve/", got)
	}
}
