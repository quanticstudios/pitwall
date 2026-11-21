package app

import (
	"errors"
	"os"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

// A chat's stashed drafts load back as saved, images included; the last
// of several quick saves wins; none left removes the file; another
// chat's file never loads for this one.
func TestStashFiles(t *testing.T) {
	f := &stashFiles{dir: t.TempDir()}
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	img := session.Attachment{ID: "image-1", Kind: "image", MimeType: "image/png", Data: "aGk="}
	if f.load("c1") != nil {
		t.Fatal("a chat with no file has drafts")
	}
	for i := range 20 {
		f.save("c1", []chatin.Stash{{Text: "draft " + strconv.Itoa(i), Attachments: []session.Attachment{img}, At: at}})
	}
	f.save("c2", []chatin.Stash{{Text: "other", At: at}})
	f.flush()
	got := f.load("c1")
	if len(got) != 1 || got[0].Text != "draft 19" || len(got[0].Attachments) != 1 || got[0].Attachments[0].Data != "aGk=" || !got[0].At.Equal(at) {
		t.Fatalf("loaded %+v", got)
	}
	if fresh := (&stashFiles{dir: f.dir}).load("c2"); len(fresh) != 1 || fresh[0].Text != "other" {
		t.Fatalf("after a restart: %+v", fresh)
	}
	// A clash of names reads as no drafts.
	data, _ := os.ReadFile(f.path("c2"))
	if err := os.WriteFile(f.path("c3"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if f.load("c3") != nil {
		t.Fatal("another chat's file loaded")
	}
	f.save("c1", nil)
	f.flush()
	if _, err := os.Stat(f.path("c1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the emptied file stays: %v", err)
	}
	if st, err := os.Stat(f.path("c2")); err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 { // Windows has no mode bits
		t.Fatalf("the file's mode: %v %v", st.Mode(), err)
	}
}

// A composer starts with its chat's stashed drafts, and a stash made in
// it reaches the file.
func TestComposerStashes(t *testing.T) {
	u := &ui{stashes: &stashFiles{dir: t.TempDir()}}
	st := model.State{Chats: []model.Chat{{ID: "c1"}}}
	u.stashes.save(stashKey("c1"), []chatin.Stash{{Text: "kept"}})
	u.stashes.flush()
	c := u.composer(&st, "c1")
	list, _ := c.StashChanged()
	if len(list) != 1 || list[0].Text != "kept" {
		t.Fatalf("the composer started with %+v", list)
	}
}

// Ctrl+S, Ctrl+Shift+Enter and the edit-queued chord reach a focused
// chat composer in every preset: the window takes none of them, but
// Alt+Up in aide (prev_tab), where Alt+Shift+Up edits instead. keep_peek's
// Ctrl+Shift+Enter is the window's only while a peek shows, when the
// composer has no keys.
func TestComposerChords(t *testing.T) {
	for _, name := range []string{"aide", "conventional", "mac"} {
		n := nav{keys: config.Preset(name)}
		var st model.State
		fs := n.keyFilters(&st)
		takes := func(name key.Name, mods key.Modifiers) bool {
			return slices.ContainsFunc(fs, func(f key.Filter) bool {
				return f.Name == name && mods&f.Required == f.Required && mods&^(f.Required|f.Optional) == 0
			})
		}
		mods := queueEditMods(n.bind())
		for _, c := range []struct {
			name key.Name
			mods key.Modifiers
		}{
			{"S", key.ModCtrl}, {key.NameReturn, key.ModShortcut | key.ModShift}, {key.NameReturn, key.ModShortcut},
			{key.NameUpArrow, mods}, {key.NameUpArrow, 0}, {key.NameDownArrow, 0},
		} {
			if takes(c.name, c.mods) {
				t.Errorf("%s: the window takes %v+%s", name, c.mods, c.name)
			}
		}
		if want := name == "aide"; (mods == key.ModAlt|key.ModShift) != want {
			t.Errorf("%s: edit-queued mods %v", name, mods)
		}
	}
}
