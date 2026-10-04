package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// JevURL is TypeSafe's System One endpoint. The key is only ever sent here.
const JevURL = "https://api.typesafe.ai/v1/systemone"

// JevModel is the model alias used when the config names none.
const JevModel = "jev-latest"

// KeysURL is where people get a TypeSafe API key.
const KeysURL = "https://console.typesafe.ai/keys"

// Secret is a string that prints as [redacted], so a key in a struct never
// reaches a log line or a panic through fmt.
type Secret string

func (Secret) String() string   { return "[redacted]" }
func (Secret) GoString() string { return "[redacted]" }

// Jev asks TypeSafe's Jev over HTTPS.
type Jev struct {
	Key   Secret
	Model string // "" means JevModel
	url   string // tests point it at a fake server
	http  *http.Client
}

// NewJev returns a provider for key and model ("" for JevModel).
func NewJev(key, model string) *Jev {
	return &Jev{Key: Secret(key), Model: model}
}

func (j *Jev) client() *http.Client {
	if j.http != nil {
		return j.http
	}
	return &http.Client{
		// The key must not follow a redirect to any other address.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Ask posts the request and decodes the answers.
func (j *Jev) Ask(ctx context.Context, r Request) (map[string]Answer, error) {
	if j.Key == "" {
		return nil, errors.New("no TypeSafe API key")
	}
	model := j.Model
	if model == "" {
		model = JevModel
	}
	body, err := json.Marshal(struct {
		Model string `json:"model"`
		Request
	}{model, r})
	if err != nil {
		return nil, err
	}
	url := j.url
	if url == "" {
		url = JevURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(j.Key))
	req.Header.Set("Content-Type", "application/json")
	res, err := j.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("jev: no answer within the timeout")
		}
		return nil, fmt.Errorf("jev: %w", stripURL(err))
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("jev: read reply: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev: %s", statusText(res.StatusCode, data, string(j.Key)))
	}
	return decodeAnswers(data)
}

// stripURL keeps the reason of a transport error without the request it
// wraps.
func stripURL(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) && ue.Unwrap() != nil {
		return ue.Unwrap()
	}
	return err
}

// statusText explains an HTTP error in a line, with the reply's message
// scrubbed of the key and anything else that looks secret.
func statusText(code int, body []byte, key string) string {
	var hint string
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint = "the API key was refused"
	case http.StatusUnprocessableEntity:
		hint = "the request was refused as invalid"
	case http.StatusTooManyRequests:
		hint = "rate limited by TypeSafe"
	case 529:
		hint = "TypeSafe is overloaded"
	default:
		hint = http.StatusText(code)
	}
	msg := strings.Join(strings.Fields(string(body)), " ")
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	out := fmt.Sprintf("HTTP %d, %s", code, hint)
	if msg != "" {
		out += ": " + msg
	}
	return Redact(out, key)
}

func decodeAnswers(data []byte) (map[string]Answer, error) {
	var reply struct {
		Answers map[string]Answer `json:"answers"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("bad reply: %w", err)
	}
	if reply.Answers == nil {
		return nil, errors.New("bad reply: no answers")
	}
	return reply.Answers, nil
}

// Ping makes one small real call to p, within timeout, and reports how
// long it took. secrets are scrubbed from the error.
func Ping(ctx context.Context, p Provider, timeout time.Duration, secrets ...string) (time.Duration, error) {
	return Test(ctx, &Client{P: p, Timeout: timeout, Secrets: secrets})
}

// Test makes one small real call and reports how long it took.
func Test(ctx context.Context, c *Client) (time.Duration, error) {
	start := time.Now()
	_, err := c.Ask(ctx, FeatureTest, "", "pitwall connection test", map[string]Question{
		"ok": {Type: Noul, Instructions: "Is this text a connection test?"},
	})
	return time.Since(start), err
}
