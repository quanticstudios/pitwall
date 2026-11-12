package mobile

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"image"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// What the clipboard held when it held no picture. The composer's Paste
// pastes text for errClipText and errClipLink.
var (
	errClipEmpty = errors.New("the clipboard is empty")
	errClipText  = errors.New("the clipboard holds text, not a picture")
	errClipLink  = errors.New("the clipboard holds a link, not a picture")
)

var imgSrc = regexp.MustCompile(`(?i)<img\b[^>]*?\ssrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// clipPictures lists the URIs to try for a picture from the clipboard as
// PhotoPick.describe writes it: lines of a kind (mime, item, uri, text,
// html) and a URI-encoded value. Per item, its own URI comes first, whatever
// the clip's MIME types say, then each content:// or file:// line of its
// text (a text/uri-list too), then each <img> in its HTML. With none, the
// error says what the clipboard holds.
func clipPictures(desc string) ([]string, error) {
	found := errClipEmpty
	var uris []string
	add := func(u string) {
		if !slices.Contains(uris, u) {
			uris = append(uris, u)
		}
	}
	for _, line := range strings.Split(desc, "\n") {
		kind, enc, _ := strings.Cut(line, " ")
		v, err := url.PathUnescape(enc)
		if err != nil {
			continue
		}
		switch kind {
		case "uri":
			if local(v) {
				add(v)
			} else {
				found = errClipLink
			}
		case "text":
			for _, l := range strings.Split(v, "\n") {
				l = strings.TrimSpace(l)
				if local(l) {
					add(l)
				} else if l != "" && !strings.HasPrefix(l, "#") && found != errClipLink {
					found = errClipText
				}
			}
		case "html":
			for _, m := range imgSrc.FindAllStringSubmatch(v, -1) {
				if src := html.UnescapeString(m[1] + m[2] + m[3]); local(src) {
					add(src)
				}
			}
		}
	}
	if len(uris) == 0 {
		return nil, found
	}
	return uris, nil
}

// local reports whether s is a content:// or file:// URI, the ones
// ContentResolver opens.
func local(s string) bool {
	return strings.HasPrefix(s, "content://") || strings.HasPrefix(s, "file://")
}

// clipShape is desc without its values, for the log: the MIME types, and each
// value's kind with a URI's scheme.
func clipShape(desc string) string {
	var b strings.Builder
	for _, line := range strings.Split(desc, "\n") {
		kind, enc, _ := strings.Cut(line, " ")
		if kind == "" {
			continue
		}
		b.WriteString(kind)
		if v, err := url.PathUnescape(enc); err == nil && (kind == "mime" || kind == "uri") {
			if kind == "uri" {
				v, _, _ = strings.Cut(v, ":")
			}
			b.WriteString(" " + v)
		}
		b.WriteString("; ")
	}
	return strings.TrimSuffix(b.String(), "; ")
}

// isPicture reports whether b decodes as an image newAttachment reads.
func isPicture(b []byte) bool {
	_, _, err := image.DecodeConfig(bytes.NewReader(b))
	return err == nil
}

// checkPicture rejects bytes that are no image when the provider named a
// type that is no image either; other bytes go on to newAttachment.
func checkPicture(b []byte, mime string) error {
	if mime == "" || strings.HasPrefix(mime, "image/") {
		return nil
	}
	if isPicture(b) {
		return nil
	}
	return fmt.Errorf("it is a %s file, not a picture", mime)
}
