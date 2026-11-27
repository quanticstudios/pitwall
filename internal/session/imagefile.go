package session

import (
	"encoding/base64"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// maxImageFile is the largest picture ImageFile reads; Clip drops a
// smaller one's bytes from a snapshot anyway.
const maxImageFile = 20 << 20

// ImageFile is the picture at path as the n-th image attachment of a
// prompt, the way a transcript that keeps a prompt's pictures as files
// (Antigravity's, Codex's) carries them; false when the file is gone, too
// big or no image. mime is the transcript's type for it, "" to sniff it.
func ImageFile(path, mime string, n int) (Attachment, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxImageFile {
		return Attachment{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Attachment{}, false
	}
	if !strings.HasPrefix(mime, "image/") {
		mime = http.DetectContentType(b)
	}
	if !strings.HasPrefix(mime, "image/") {
		return Attachment{}, false
	}
	id := "image-" + strconv.Itoa(n)
	return Attachment{ID: id, Kind: "image", MimeType: mime, Name: id + "." + strings.TrimPrefix(mime, "image/"),
		SizeBytes: int64(len(b)), Data: base64.StdEncoding.EncodeToString(b)}, true
}
