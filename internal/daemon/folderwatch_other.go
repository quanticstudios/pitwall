//go:build !linux

package daemon

import "sync/atomic"

// folderWatch is Linux's inotify; elsewhere a pane stream reads at its
// tick.
type folderWatch struct{}

// foldersWatched counts the folders watched, for tests: none here.
var foldersWatched atomic.Int64

func newFolderWatch(func()) *folderWatch { return nil }

func (*folderWatch) Set([]string) {}
func (*folderWatch) Close()       {}
