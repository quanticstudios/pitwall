package app

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// openChatTarget opens a link a chat's transcript clicked: a URL, a file
// URL from a reply included, as openLink does; a tool row's file path the
// way a pane's path hint opens one, in the open_path popup when it is
// set, else with the desktop opener.
func (u *ui) openChatTarget(st *model.State, chat, dir, target string) {
	if strings.Contains(target, "://") {
		openLink(target)
		return
	}
	if c := findChat(st, chat); len(u.cfg.OpenPath) > 0 && c != nil && u.link().Level >= proto.PopupPathLevel {
		u.send(proto.OpenPopup{WorkspaceID: c.WorkspaceID, Cmd: u.cfg.OpenPath, Dir: dir, Path: target})
		return
	}
	if curHost() != "" || !filepath.IsAbs(target) {
		log.Printf("chat: %s is not on this machine", target)
		return
	}
	if _, err := os.Stat(target); err != nil {
		log.Printf("chat: open %s: %v", target, err)
		return
	}
	if err := hintOpen(fileURL(target)); err != nil {
		log.Printf("chat: open %s: %v", target, err)
	}
}
