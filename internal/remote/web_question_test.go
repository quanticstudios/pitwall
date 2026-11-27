//go:build unix

package remote

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestWebQuestion runs the phone page in headless Chromium against a
// fake server, and testdata/question_drive.js taps and types on its
// question card. A tap that leaves no question open sends the card and
// shows its answer at once, a second tap sends nothing, the box answers
// the open question, and a draft from before the card, a slash command,
// or a box sent while an approval waits too go out as messages. Skipped
// without Chromium.
func TestWebQuestion(t *testing.T) {
	p := newWebPage(t, "testdata/question_drive.js")
	var got struct{ Placeholder, Shown, BothPlaceholder, Error string }
	json.Unmarshal([]byte(p.drive(t, "#pane=c")), &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	if got.Placeholder != "Pick an answer above, or type your own" || !strings.Contains(got.Shown, "Answered: Blue") || got.BothPlaceholder == got.Placeholder {
		t.Fatalf("page: %+v", got)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	want := []string{
		`/api/input {"chat":"c","request":"i1","answers":{"q":["Blue"]}}`,
		`/api/input {"chat":"c","request":"i2","answers":{"a":["Red"],"b":["Blue","Green"]}}`,
		`/api/input {"chat":"c","request":"i2b","answers":{"c":["Red"],"t":["Mushrooms"],"s":["Large"]}}`,
		`/api/send {"pane":"c","text":"a draft from before","image":""}`,
		`/api/send {"pane":"c","text":"/compact","image":""}`,
		`/api/send {"pane":"c","text":"both wait","image":""}`,
	}
	if !reflect.DeepEqual(p.calls, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(p.calls, "\n"), strings.Join(want, "\n"))
	}
}

// webPage is the phone page served with a drive script after app.js, by
// a fake server whose stream says hello. It notes the POSTs to /api/send
// and /api/input, and the script POSTs what it found to /result.
type webPage struct {
	url     string
	browser string
	result  chan string
	mu      sync.Mutex
	calls   []string
	views   bool // record POST /api/view too
}

// newWebPage serves the page with the drive script at path; it skips the
// test without Chromium.
func newWebPage(t *testing.T, path string) *webPage {
	t.Helper()
	p := &webPage{result: make(chan string, 1)}
	for _, b := range []string{"google-chrome", "chromium", "chromium-browser"} { // CI's chromium may be a snap shim
		if b, err := exec.LookPath(b); err == nil {
			p.browser = b
			break
		}
	}
	if p.browser == "" || testing.Short() {
		t.Skip("no Chromium")
	}
	drive, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	index, _ := page.ReadFile("web/index.html")
	html := strings.Replace(string(index), `<script src="app.js"></script>`, `<script src="pre.js"></script><script src="app.js"></script><script src="drive.js"></script>`, 1)
	mux := http.NewServeMux()
	serve := func(path, ctype string, body []byte) {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			w.Write(body)
		})
	}
	js := "text/javascript"
	serve("/{$}", "text/html", []byte(html))
	serve("/pre.js", js, []byte(`localStorage.setItem('pitwall-token', 't'); const shot = new URLSearchParams(location.hash.slice(1)).get('shot');`)) // the page drops its hash at start
	serve("/drive.js", js, drive)
	for _, f := range []string{"app.js", "app.css"} {
		b, _ := page.ReadFile("web/" + f)
		serve("/"+f, map[bool]string{true: js, false: "text/css"}[strings.HasSuffix(f, ".js")], b)
	}
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: hello\ndata: {\"models\":[],\"efforts\":[]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("POST /api/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		if r.URL.Path == "/api/input" || r.URL.Path == "/api/send" || r.URL.Path == "/api/away" || r.URL.Path == "/api/view" && p.views {
			p.calls = append(p.calls, r.URL.Path+" "+strings.TrimSpace(string(b)))
		}
		p.mu.Unlock()
		io.WriteString(w, `{"id":1}`)
	})
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p.result <- string(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.url = srv.URL + "/"
	return p
}

// drive opens the page at hash and returns what its script POSTed.
func (p *webPage) drive(t *testing.T, hash string) string {
	t.Helper()
	var got string
	p.chromium(t, func(exited chan error) bool {
		select {
		case got = <-p.result:
			return true
		case err := <-exited:
			exited <- err // for the deferred wait
		case <-time.After(time.Minute):
		}
		return false
	}, p.url+hash)
	return got
}

// chromium runs headless Chromium with args until done, which waits on
// it and reports whether it got what it waited for; the test fails with
// what Chromium said otherwise.
func (p *webPage) chromium(t *testing.T, done func(exited chan error) bool, args ...string) {
	t.Helper()
	profile, err := os.MkdirTemp("", "pitwall-web-page")
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--disable-extensions", "--user-data-dir=" + profile}
	if os.Getenv("CI") != "" {
		base = append(base, "--no-sandbox") // why: CI runners' AppArmor refuses Chromium's sandbox; the page is our own, on loopback
	}
	cmd := exec.Command(p.browser, append(base, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // its helpers go with it
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited
		for range 40 { // the helpers may still be writing the profile as they die
			if os.RemoveAll(profile) == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	if !done(exited) {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // stderr is whole once it has exited
		err := <-exited
		exited <- err
		t.Fatalf("%s: %v\n%s", p.browser, err, stderr.String())
	}
}
