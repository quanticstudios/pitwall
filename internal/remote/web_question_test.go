//go:build unix

package remote

import (
	"context"
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
	var browser string
	for _, b := range []string{"chromium", "chromium-browser", "google-chrome"} {
		if p, err := exec.LookPath(b); err == nil {
			browser = p
			break
		}
	}
	if browser == "" || testing.Short() {
		t.Skip("no Chromium")
	}
	drive, err := os.ReadFile("testdata/question_drive.js")
	if err != nil {
		t.Fatal(err)
	}
	index, _ := page.ReadFile("web/index.html")
	html := strings.Replace(string(index), `<script src="app.js"></script>`, `<script src="pre.js"></script><script src="app.js"></script><script src="drive.js"></script>`, 1)
	var mu sync.Mutex
	var calls []string
	result := make(chan string, 1)
	mux := http.NewServeMux()
	serve := func(path, ctype string, body []byte) {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			w.Write(body)
		})
	}
	js := "text/javascript"
	serve("/{$}", "text/html", []byte(html))
	serve("/pre.js", js, []byte(`localStorage.setItem('pitwall-token', 't');`))
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
		if r.URL.Path == "/api/input" || r.URL.Path == "/api/send" {
			mu.Lock()
			calls = append(calls, r.URL.Path+" "+strings.TrimSpace(string(b)))
			mu.Unlock()
		}
		io.WriteString(w, `{"id":1}`)
	})
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		result <- string(b)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	profile, err := os.MkdirTemp("", "pitwall-web-question")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(browser, "--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--user-data-dir="+profile, srv.URL+"/#pane=c")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // its helpers go with it
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
		for range 40 { // the helpers may still be writing the profile as they die
			if os.RemoveAll(profile) == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	var got struct{ Placeholder, Shown, BothPlaceholder, Error string }
	select {
	case r := <-result:
		json.Unmarshal([]byte(r), &got)
	case <-ctx.Done():
		t.Fatal("the page never reported")
	}
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	if got.Placeholder != "Pick an answer above, or type your own" || !strings.Contains(got.Shown, "Answered: Blue") || got.BothPlaceholder == got.Placeholder {
		t.Fatalf("page: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		`/api/input {"chat":"c","request":"i1","answers":{"q":["Blue"]}}`,
		`/api/input {"chat":"c","request":"i2","answers":{"a":["Red"],"b":["Blue","Green"]}}`,
		`/api/send {"pane":"c","text":"a draft from before","image":""}`,
		`/api/send {"pane":"c","text":"/compact","image":""}`,
		`/api/send {"pane":"c","text":"both wait","image":""}`,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}
