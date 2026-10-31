package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Provider starts drivers of one kind. The daemon holds one per
// model.Provider it can drive.
type Provider interface {
	// Start runs the provider for one chat and returns once it takes
	// commands. emit gets every event, from one goroutine, in order, until
	// Close returns; never after.
	Start(ctx context.Context, cfg Config, from Origin, emit func(Event)) (Driver, error)
}

// Origin is where a started provider session comes from.
type Origin struct {
	Resume string `json:"resume,omitempty"` // a provider session to continue; "" starts a new one
	At     string `json:"at,omitempty"`     // a Config.Points value: continue from there, dropping what came after
	Fork   bool   `json:"fork,omitempty"`   // continue under a new provider id, leaving Resume as it was
}

// Driver is one running provider process for one chat. Every method is
// safe to call from any goroutine and returns once the provider took or
// refused the command, not when the work is done.
type Driver interface {
	// Send starts a turn with m and returns its turn id. ErrBusy while a
	// turn runs: the chat loop queues instead.
	Send(ctx context.Context, m Message) (turnID string, err error)
	// Steer adds m to the running turn. ErrUnsupported when the provider
	// cannot (the loop queues), ErrIdle with no turn.
	Steer(ctx context.Context, m Message) error
	// Interrupt stops the running turn; a no-op while idle. The turn's end
	// arrives as turn.completed with state interrupted.
	Interrupt(ctx context.Context) error
	// Approve answers request.opened requestID.
	Approve(ctx context.Context, requestID string, d Decision) error
	// Answer answers user-input.requested requestID, by question id.
	Answer(ctx context.Context, requestID string, answers map[string][]string) error
	// Set changes model, effort or permission mode; "" leaves one as it is.
	Set(ctx context.Context, s Settings) error
	// Close ends the process: politely, then killed when ctx ends.
	Close(ctx context.Context) error
}

type Message struct {
	ID     string // the user-prompt item id; Claude's stdin uuid, Codex's clientUserMessageId
	Text   string
	Images []Image
}

type Image struct {
	MimeType string // image/png, image/jpeg, image/webp
	Name     string
	Data     []byte
}

type Decision struct {
	Allow      bool
	ForSession bool            // allow this kind of call for the rest of the session
	Reason     string          // deny: what the model reads
	Input      json.RawMessage // allow: edited tool input, nil for unchanged
	// Plan approvals: Allow is Implement, a deny with Reason is Refine.
}

type Settings struct{ Model, Effort, Mode string }

// Config is a chat's config.json (see Storage).
type Config struct {
	Format    int            `json:"format"` // 1
	ID        string         `json:"id"`
	Provider  model.Provider `json:"provider"`
	Workspace string         `json:"workspace"`
	Cwd       string         `json:"cwd"`
	Model     string         `json:"model,omitempty"`
	Effort    string         `json:"effort,omitempty"`
	Mode      string         `json:"mode,omitempty"`
	// Resume is the provider session id: Claude's session_id, Codex's
	// thread id. A fork's new id replaces it as soon as the driver says.
	Resume string `json:"resume,omitempty"`
	// From is where the next start continues instead of Resume: a fork or
	// rewind whose new provider id has not arrived yet (Claude reports it
	// with the first turn). The new id clears it.
	From *Origin `json:"from,omitempty"`
	// Points maps a turn id to Event.Point, for fork and rewind.
	Points           map[string]string `json:"points,omitempty"`
	RecentPrompt     string            `json:"recentPrompt,omitempty"`
	FakeStartFailure string            `json:"-"` // fake provider only; never persisted
	Queue            []QueuedMessage   `json:"queue,omitempty"`
	Held             bool              `json:"held,omitempty"` // the queue waits for the next send (after an interrupt)
	// Continued is when the loop sent "Continue where you left off" after
	// a restart, for the limit on those sends.
	Continued []time.Time `json:"continued,omitempty"`
	Created   time.Time   `json:"created"`
	// Title and Updated keep an archived chat's title and last change,
	// which a live chat's tab and snapshot hold; archive and import set them.
	Title   string    `json:"title,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	// Imported marks a chat imported from aide's own store (ENG-2707);
	// while it stays archived, a later import replaces it.
	Imported bool `json:"imported,omitempty"`
	// Env is what the process gets on top of the daemon's environment;
	// rebuilt from the tab on every start, never saved.
	Env []string `json:"-"`
}

type QueuedMessage struct {
	CreatedAt string   `json:"createdAt,omitempty"`
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Images    []string `json:"images,omitempty"` // paths under the chat's images/ folder
}
