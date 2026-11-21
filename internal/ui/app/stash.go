package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

// stashFiles keeps each chat's stashed drafts, Ctrl+S in its composer,
// in a file of its own under the state dir, so they survive restarts.
// Writes run off the UI goroutine, the newest list of a chat winning.
// shortcut: a deleted chat's file stays; prune once stashes are listed
// outside their chat.
type stashFiles struct {
	dir  string
	mu   sync.Mutex
	next map[string][]chatin.Stash // per chat, the list still to write
	busy map[string]bool           // per chat, a write running
	wg   sync.WaitGroup
}

// stashFile is one chat's file: the chat, to catch a name clash, and its
// drafts, newest first.
type stashFile struct {
	Chat    string         `json:"chat"`
	Stashes []chatin.Stash `json:"stashes"`
}

func newStashFiles() *stashFiles {
	return &stashFiles{dir: filepath.Join(config.StateDir(), "stash")}
}

func (f *stashFiles) path(chat string) string {
	sum := sha256.Sum256([]byte(chat))
	return filepath.Join(f.dir, hex.EncodeToString(sum[:12])+".json")
}

// load is chat's stashed drafts, the one still to write if any.
func (f *stashFiles) load(chat string) []chatin.Stash {
	f.mu.Lock()
	l, ok := f.next[chat]
	f.mu.Unlock()
	if ok {
		return l
	}
	data, err := os.ReadFile(f.path(chat))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var file stashFile
	if err == nil {
		err = json.Unmarshal(data, &file)
	}
	if err != nil {
		log.Printf("stash: %q", err)
		return nil
	}
	if file.Chat != chat {
		return nil
	}
	return file.Stashes
}

// save writes chat's stashed drafts, removing its file once none is left.
func (f *stashFiles) save(chat string, list []chatin.Stash) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next == nil {
		f.next, f.busy = map[string][]chatin.Stash{}, map[string]bool{}
	}
	f.next[chat] = list
	if f.busy[chat] {
		return
	}
	f.busy[chat] = true
	f.wg.Add(1)
	go f.write(chat)
}

func (f *stashFiles) write(chat string) {
	defer f.wg.Done()
	for {
		f.mu.Lock()
		list, ok := f.next[chat]
		if !ok {
			delete(f.busy, chat)
			f.mu.Unlock()
			return
		}
		delete(f.next, chat)
		f.mu.Unlock()
		var err error
		if len(list) == 0 {
			if err = os.Remove(f.path(chat)); errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		} else {
			data, _ := json.Marshal(stashFile{chat, list})
			err = writeAtomic(f.path(chat), data)
		}
		if err != nil {
			log.Printf("stash: %q", err)
		}
	}
}

// flush waits for the writes running.
func (f *stashFiles) flush() { f.wg.Wait() }
