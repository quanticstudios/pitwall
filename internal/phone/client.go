package phone

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/repoicon"
)

// ErrUnpaired is a refused device token: pitwall revoked this phone.
var ErrUnpaired = errors.New("this phone is no longer paired; pair it again")

// UntrustedError is a server certificate that neither the system nor the
// pin trusts, such as the one pitwall makes for tls = true.
type UntrustedError struct{ Fingerprint string }

func (e *UntrustedError) Error() string {
	return "this server's certificate is not trusted; its SHA-256 is " + e.Fingerprint
}

// Fingerprint is der's SHA-256 as pitwall remote pair prints it.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	hexs := make([]string, len(sum))
	for i, b := range sum {
		hexs[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(hexs, ":")
}

// Client calls the phone page's API with a device token.
type Client struct {
	Creds
	http *http.Client
}

// NewClient is a client for c. With c.Pin set it trusts exactly the
// certificate whose SHA-256 that is, and no other.
func NewClient(c Creds) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.Pin != "" {
		pin := c.Pin
		tr.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, // why: VerifyConnection checks the pin in place of the chain.
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 || !SamePin(Fingerprint(cs.PeerCertificates[0].Raw), pin) {
					return errors.New("the server's certificate changed since you trusted it; pair again")
				}
				return nil
			},
		}
	}
	return &Client{Creds: c, http: &http.Client{Transport: tr, CheckRedirect: sameOrigin(c.Server)}}
}

// sameOrigin refuses a redirect off server's scheme, host and port: it
// would carry the device token and the body there, in plain text after a
// switch to http, where the pin guards nothing.
func sameOrigin(server string) func(*http.Request, []*http.Request) error {
	return func(r *http.Request, via []*http.Request) error {
		u, err := url.Parse(server)
		if err != nil || r.URL.Scheme != u.Scheme || !strings.EqualFold(r.URL.Host, u.Host) {
			return fmt.Errorf("refused a redirect to %s://%s, off the paired address", r.URL.Scheme, r.URL.Host)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
}

// untrusted turns a failed certificate check into an UntrustedError.
func untrusted(err error) error {
	var ce *tls.CertificateVerificationError
	if errors.As(err, &ce) && len(ce.UnverifiedCertificates) > 0 {
		var ua x509.UnknownAuthorityError
		var hn x509.HostnameError
		if errors.As(err, &ua) || errors.As(err, &hn) {
			return &UntrustedError{Fingerprint: Fingerprint(ce.UnverifiedCertificates[0].Raw)}
		}
	}
	return err
}

// Pair trades code for a device token at server, as the page does.
func Pair(ctx context.Context, server, code, pin string) (Creds, error) {
	c := NewClient(Creds{Server: server, Pin: pin})
	var out struct {
		Token  string `json:"token"`
		Device string `json:"device"`
	}
	if err := c.call(ctx, "POST", "api/pair", map[string]string{"code": code}, &out); err != nil {
		if errors.Is(err, ErrUnpaired) {
			return Creds{}, errors.New("that code was wrong, used or expired. Run pitwall remote pair again for a new one")
		}
		return Creds{}, err
	}
	return Creds{Server: server, Token: out.Token, Device: out.Device, Pin: pin}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.Server+path, rd)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r, nil
}

// call sends body as JSON (GET when nil) and decodes the answer into out.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	r, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return untrusted(err)
	}
	defer resp.Body.Close()
	if err := status(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// status is resp's failure, with the server's own words.
func status(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnpaired
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if msg := strings.TrimSpace(string(b)); msg != "" {
			return errors.New(msg)
		}
		return errors.New(resp.Status)
	}
	return nil
}

// ErrNoIcon is Icon's error for a hash the machine does not serve, as from
// a daemon too old to serve any.
var ErrNoIcon = errors.New("no such icon")

// Icon fetches the repo icon of hash, as Tab.Icon names it: its bytes and
// whether it is an SVG. Anything but a PNG, ICO or SVG of at most
// repoicon.Max bytes is refused.
func (c *Client) Icon(ctx context.Context, hash string) (data []byte, svg bool, err error) {
	r, err := c.request(ctx, "GET", "api/icon/"+url.PathEscape(hash), nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, false, untrusted(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, ErrNoIcon
	}
	if err := status(resp); err != nil {
		return nil, false, err
	}
	switch resp.Header.Get("Content-Type") {
	case "image/png", "image/x-icon":
	case "image/svg+xml":
		svg = true
	default:
		return nil, false, ErrNoIcon
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, repoicon.Max+1))
	if err == nil && len(data) > repoicon.Max {
		return nil, false, ErrNoIcon
	}
	return data, svg, err
}

// Send types or queues text for pane's agent and returns the message's id.
func (c *Client) Send(ctx context.Context, pane, text string) (uint64, error) {
	var out struct {
		ID uint64 `json:"id"`
	}
	err := c.call(ctx, "POST", "api/send", map[string]string{"pane": pane, "text": text}, &out)
	return out.ID, err
}

// Command picks a Claude Code pane's model or effort: what is "model" or
// "effort".
func (c *Client) Command(ctx context.Context, pane, what, value string) error {
	return c.call(ctx, "POST", "api/command", map[string]string{"pane": pane, "what": what, "value": value}, nil)
}

// IsBusy reports whether err is a refusal because the agent is in a turn
// or waits on the user, which goes away when the turn ends.
//
// ponytail: matches the daemon's words (typeCommand); a status code of
// its own if another refusal ever says the same.
func IsBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "try again when it waits at its input box")
}

// Answer presses Allow or Deny on pane's permission prompt of at.
func (c *Client) Answer(ctx context.Context, pane string, at int64, allow bool) error {
	return c.call(ctx, "POST", "api/answer", map[string]any{"pane": pane, "at": strconv.FormatInt(at, 10), "allow": allow}, nil)
}

// Approve answers a chat tab's approval: allow, allow-session or deny.
func (c *Client) Approve(ctx context.Context, chat, request, decision, reason string) error {
	return c.call(ctx, "POST", "api/approve", map[string]string{"chat": chat, "request": request, "decision": decision, "reason": reason}, nil)
}

// Input answers a chat tab's input request, by question id.
func (c *Client) Input(ctx context.Context, chat, request string, answers map[string][]string) error {
	return c.call(ctx, "POST", "api/input", map[string]any{"chat": chat, "request": request, "answers": answers}, nil)
}

// Older is pane's page of entries before seq, and whether more come before.
func (c *Client) Older(ctx context.Context, pane string, before int64) ([]flow.Entry, bool, error) {
	var out struct {
		Entries []flow.Entry `json:"entries"`
		More    bool         `json:"more"`
	}
	q := url.Values{"pane": {pane}, "before": {strconv.FormatInt(before, 10)}}
	err := c.call(ctx, "GET", "api/chat?"+q.Encode(), nil, &out)
	return out.Entries, out.More, err
}

// File is the markdown file, or an image one shows, at path in tab's
// folder, a relative path read from the tab's working directory, as the
// computer reads it.
func (c *Client) File(ctx context.Context, tab, path string) ([]byte, error) {
	r, err := c.request(ctx, "GET", "api/file?"+url.Values{"tab": {tab}, "path": {path}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, untrusted(err)
	}
	defer resp.Body.Close()
	if err := status(resp); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, remote.MaxFileBytes+1))
	if err == nil && len(b) > remote.MaxFileBytes {
		err = fmt.Errorf("larger than %d MiB", remote.MaxFileBytes>>20)
	}
	return b, err
}

// Hello is the stream's first event. Files is set by a computer that
// serves File.
type Hello struct {
	Device  string   `json:"device"`
	Version string   `json:"version"` // the daemon's build; "" from an older daemon
	Models  []string `json:"models"`
	Efforts []string `json:"efforts"`
	Upload  bool     `json:"upload"`
	Files   bool     `json:"files"`
	// Latency is how long the stream's request took to get its answer's
	// first byte over an open connection: a round trip and the server's
	// check of the token.
	Latency time.Duration `json:"-"`
}

// Entries is a "reset" or "entries" event of the open chat.
type Entries struct {
	Entries []flow.Entry `json:"entries"`
	Model   string       `json:"model"`
	More    bool         `json:"more"` // reset: older entries are on the server
	None    bool         `json:"none"` // reset: the tab has no transcript
	Gone    bool         `json:"gone"` // reset: the tab closed
}

// Event is one event of the stream: Name is hello, tabs, reset or entries,
// and the field of that name holds it. Pane is the chat the stream was
// opened for.
type Event struct {
	Name    string
	Pane    string
	Hello   Hello
	Tabs    []remote.Tab
	Entries Entries
}

// streamIdle drops a stream that sent nothing, not even the server's ping
// every 20 seconds, for this long: a phone that slept can leave a dead
// connection that never errors.
var streamIdle = 45 * time.Second

// Stream reads the event stream, with pane's chat when pane is set, and
// calls on for each event until the stream ends or ctx is done.
func (c *Client) Stream(ctx context.Context, pane string, on func(Event)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	path := "api/stream"
	if pane != "" {
		path += "?" + url.Values{"pane": {pane}}.Encode()
	}
	r, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	idle := time.AfterFunc(streamIdle, cancel)
	defer idle.Stop()
	var conn, first time.Time
	r = r.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn:              func(httptrace.GotConnInfo) { conn = time.Now() },
		GotFirstResponseByte: func() { first = time.Now() },
	}))
	resp, err := c.http.Do(r)
	if err != nil {
		return untrusted(err)
	}
	defer resp.Body.Close()
	if err := status(resp); err != nil {
		return err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	name, data := "", []byte(nil)
	for sc.Scan() {
		idle.Reset(streamIdle)
		line := sc.Text()
		switch {
		case line == "":
			if e, ok := decodeEvent(name, data); ok {
				e.Pane = pane
				if e.Name == "hello" && !conn.IsZero() && first.After(conn) {
					e.Hello.Latency = first.Sub(conn)
				}
				on(e)
			}
			name, data = "", data[:0]
		case strings.HasPrefix(line, "event: "):
			name = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			data = append(data, line[len("data: "):]...)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func decodeEvent(name string, data []byte) (Event, bool) {
	e := Event{Name: name}
	var v any
	switch name {
	case "hello":
		v = &e.Hello
	case "tabs":
		v = &e.Tabs
	case "reset", "entries":
		v = &e.Entries
	default:
		return e, false
	}
	return e, json.Unmarshal(data, v) == nil
}

// Question answers the exact terminal question page shown by the stream.
func (c *Client) Question(ctx context.Context, pane, id string, option int, text string) error {
	return c.call(ctx, "POST", "api/question", map[string]any{"pane": pane, "id": id, "option": option, "text": text}, nil)
}

// Upload saves an image for pane and returns its attachment name.
func (c *Client) Upload(ctx context.Context, pane string, data []byte) (string, error) {
	r, err := c.request(ctx, "POST", "api/upload?pane="+url.QueryEscape(pane), nil)
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(r)
	if err != nil {
		return "", untrusted(err)
	}
	defer resp.Body.Close()
	if err := status(resp); err != nil {
		return "", err
	}
	var out struct {
		Image string `json:"image"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out)
	return out.Image, err
}

func (c *Client) SendImage(ctx context.Context, pane, text, image string) (uint64, error) {
	var out struct {
		ID uint64 `json:"id"`
	}
	err := c.call(ctx, "POST", "api/send", map[string]string{"pane": pane, "text": text, "image": image}, &out)
	return out.ID, err
}

// OpenDesktop focuses pane in an existing desktop window.
func (c *Client) OpenDesktop(ctx context.Context, pane string) error {
	return c.call(ctx, "POST", "api/open", map[string]string{"pane": pane}, nil)
}
