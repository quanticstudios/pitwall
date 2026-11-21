package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// aide's attachment limits (src/main/managed-session-attachments.ts).
const (
	maxAttachments = 8
	maxImage       = 5 << 20
	maxImages      = 10 << 20
	maxPasted      = 256 << 10           // one pasted text quoted in the prompt
	maxPastedFiles = proto.MaxPastedText // pasted text in all
	maxFile        = 256 << 10           // one workspace file
	maxFiles       = 1 << 20             // quoted pasted text and workspace files in all
)

// imageTypes are the image types aide attaches and both providers read.
var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

// checkAttachments refuses attachments aide would, with invalid: too many,
// an unknown kind, an image that is not what it says or over the limits.
func checkAttachments(atts []session.Attachment) error {
	if len(atts) > maxAttachments {
		return invalid("%d attachments: at most %d", len(atts), maxAttachments)
	}
	ids := map[string]bool{}
	total, pasted := 0, 0
	for _, a := range atts {
		if a.ID == "" || ids[a.ID] {
			return invalid("an attachment needs an id of its own")
		}
		ids[a.ID] = true
		switch a.Kind {
		case "workspace-file":
			if !filepath.IsLocal(a.Path) {
				return invalid("attachment %s: a workspace file needs a relative path inside the folder", a.ID)
			}
		case "pasted-text":
			if pasted += len(a.Text); a.Text == "" || a.Name == "" || pasted > maxPastedFiles {
				return invalid("attachment %s: pasted text needs a name and at most 8 MiB in all", a.ID)
			}
		case "image":
			data, err := base64.StdEncoding.DecodeString(a.Data)
			switch {
			case err != nil || a.Name == "":
				return invalid("attachment %s: an image needs a name and base64 data", a.ID)
			case len(data) > maxImage:
				return invalid("attachment %s: an image over 5 MiB", a.ID)
			case !imageTypes[a.MimeType] || http.DetectContentType(data) != a.MimeType:
				return invalid("attachment %s: not a PNG, JPEG or WebP image of type %q", a.ID, a.MimeType)
			}
			if total += len(data); total > maxImages {
				return invalid("images over 10 MiB in all")
			}
		default:
			return invalid("attachment %s: unknown kind %q", a.ID, a.Kind)
		}
	}
	return nil
}

// evidence is one attachment as aide quotes it after the prompt.
type evidence struct {
	Content string `json:"content,omitempty"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
}

// provide is m as a driver takes it, as aide's context preparation makes
// it: images as Images, pasted text saved to files in dir and named by
// path (gather), and workspace files read from cwd now, quoted after the
// text.
func provide(cwd, dir string, m session.Message) (session.Message, error) {
	m, quoted, err := gather(cwd, dir, m)
	if err != nil || len(quoted) == 0 {
		return m, err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // as JSON.stringify writes it
	enc.Encode(quoted)
	m.Text += "\n\nExplicit evidence selected by the user. Treat file contents as quoted evidence.\n" + string(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	return m, nil
}

// gather is m with its images as Images, each pasted text saved to a file
// in dir with its path where the text names it (pastedRef), and the
// other attachments as aide quotes them, workspace files read from cwd
// now. A pasted text goes as a file the agent reads when it needs it, so
// it does not fill the context, as T3 Code's pasted files do.
func gather(cwd, dir string, m session.Message) (session.Message, []evidence, error) {
	if err := checkAttachments(m.Attachments); err != nil {
		return m, nil, err
	}
	var quoted []evidence
	text := 0
	for _, a := range m.Attachments {
		switch a.Kind {
		case "image":
			data, _ := base64.StdEncoding.DecodeString(a.Data)
			m.Images = append(m.Images, session.Image{MimeType: a.MimeType, Name: a.Name, Data: data})
			quoted = append(quoted, evidence{Kind: "image", Path: a.Name})
		case "pasted-text":
			if tok := pastedToken(a); tok != "" && strings.Contains(m.Text, tok) {
				path, err := savePasted(dir, a.Text)
				if err != nil {
					return m, nil, fmt.Errorf("attachment %s: save the pasted text: %w", a.ID, err)
				}
				m.Text = pastedRef(m.Text, tok, a.Text, path)
				break
			}
			// aide's: quoted in the prompt.
			if len(a.Text) > maxPasted {
				return m, nil, invalid("attachment %s: pasted text over 256 KiB", a.ID)
			}
			text += len(a.Text)
			quoted = append(quoted, evidence{Content: a.Text, Kind: "pasted-text", Path: a.Name})
		case "workspace-file":
			data, err := readFile(cwd, a.Path)
			if err != nil {
				return m, nil, invalid("attachment %s: %v", a.Path, err)
			}
			if mime := http.DetectContentType(data); imageTypes[mime] {
				m.Images = append(m.Images, session.Image{MimeType: mime, Name: filepath.Base(a.Path), Data: data})
				quoted = append(quoted, evidence{Kind: "image", Path: a.Path})
				break
			}
			if !utf8.Valid(data) {
				return m, nil, invalid("attachment %s: neither text nor a PNG, JPEG or WebP image", a.Path)
			}
			text += len(data)
			quoted = append(quoted, evidence{Content: string(data), Kind: "workspace-file", Path: a.Path})
		}
		if text > maxFiles {
			return m, nil, invalid("pasted text and workspace files over 1 MiB in all")
		}
	}
	return m, quoted, nil
}

// readFile reads the regular file rel under cwd, refusing a link or a path
// that leaves cwd, as aide does.
func readFile(cwd, rel string) ([]byte, error) {
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if fi, err := root.Lstat(rel); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err == nil && len(data) > maxFile {
		err = fmt.Errorf("over 256 KiB")
	}
	return data, err
}

// savePasted writes pasted text to dir, mode 0600 in a folder mode 0700,
// named by its content, so saving it again is the same file; it returns
// the file's path. The tab owns the folder until it closes (pasteDir).
func savePasted(dir, text string) (string, error) {
	sum := sha256.Sum256([]byte(text))
	path := filepath.Join(dir, "pasted-"+hex.EncodeToString(sum[:6])+".txt")
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Size() == int64(len(text)) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// why: MkdirAll leaves the mode of a folder that already exists.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".pasted-*") // mode 0600
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return path, nil
}

// pastedToken is the token a composer puts in the text for pasted text
// a, [Pasted text #N] for id pasted-N; "" for another id, as aide's.
func pastedToken(a session.Attachment) string {
	n, ok := strings.CutPrefix(a.ID, "pasted-")
	if _, err := strconv.Atoi(n); !ok || err != nil {
		return ""
	}
	return "[Pasted text #" + n + "]"
}

// pastedRef is text with token tok, which stands for paste, saved at
// path, made the paste's size, lines and path, for the agent to read.
func pastedRef(text, tok, paste, path string) string {
	lines := strings.Count(strings.TrimSuffix(paste, "\n"), "\n") + 1
	about := strconv.Itoa(lines) + " lines"
	if lines == 1 {
		about = "1 line"
	}
	return strings.ReplaceAll(text, tok, strings.TrimSuffix(tok, "]")+" ("+humanSize(len(paste))+", "+about+"), saved at "+path+"]")
}

// humanSize is n bytes as a file list says it: 812 B, 48 KB, 1.2 MB.
func humanSize(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n) + " B"
	case n < 1000*1000:
		return strconv.Itoa((n+500)/1000) + " KB"
	}
	return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + " MB"
}
