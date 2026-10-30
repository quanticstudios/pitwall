// SPDX-License-Identifier: Unlicense OR MIT

package app

import (
	"image"
	"net/url"
	"strings"
)

// FileDropEvent is pitwall's native file drop event. Position is relative
// to the content area, in pixels. Paths are local, decoded file paths.
type FileDropEvent struct {
	Position image.Point
	Paths    []string
}

func (FileDropEvent) ImplementsEvent() {}

// fileURIs accepts local files only. A file manager's URI list may contain
// comments, CRLF separators, percent escapes and a localhost authority.
func fileURIs(data string) []string {
	var paths []string
	for line := range strings.Lines(data) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "file" || u.Host != "" && u.Host != "localhost" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") || strings.ContainsRune(u.Path, 0) {
			continue
		}
		paths = append(paths, u.Path)
	}
	return paths
}
