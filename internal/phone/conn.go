package phone

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Backoff is the wait before each retry: Min, then twice the last, up to
// Max, as the page waits 1, 2, 4 and so on up to 15 seconds.
type Backoff struct {
	Min, Max time.Duration
	next     time.Duration
}

// Next is the wait before the next try.
func (b *Backoff) Next() time.Duration {
	if b.next == 0 {
		b.next = b.Min
	}
	d := b.next
	b.next = min(2*b.next, b.Max)
	return d
}

// Reset starts the waits over, after a try that worked.
func (b *Backoff) Reset() { b.next = 0 }

// Conn keeps one stream open, for the chat Watch names, and opens it
// again after it drops, waiting by Backoff. Its callbacks run on its own
// goroutine.
type Conn struct {
	Client *Client
	// On gets each event; Status gets the connection's state changes:
	// online, or offline with why and when it tries again (zero when it
	// does not). ErrUnpaired stops the Conn for good.
	On     func(Event)
	Status func(online bool, err error, retry time.Time)
	Wait   Backoff

	mu     sync.Mutex
	pane   string
	cancel context.CancelFunc // the open stream's
	again  chan struct{}      // retry now
	stop   context.CancelFunc
}

// Start opens the stream for pane, "" for the tab list alone.
func (k *Conn) Start(pane string) {
	if k.Wait.Min == 0 {
		k.Wait = Backoff{Min: time.Second, Max: 15 * time.Second}
	}
	ctx, stop := context.WithCancel(context.Background())
	k.pane, k.again, k.stop = pane, make(chan struct{}, 1), stop
	go k.run(ctx)
}

// Watch moves the stream to pane's chat, "" for none, at once.
func (k *Conn) Watch(pane string) {
	k.mu.Lock()
	k.pane = pane
	k.mu.Unlock()
	k.Retry()
}

// Retry opens the stream again now, with the waits started over: the app
// came back to the foreground, where a stream from before may be dead.
func (k *Conn) Retry() {
	k.mu.Lock()
	if k.cancel != nil {
		k.cancel()
	}
	k.mu.Unlock()
	select {
	case k.again <- struct{}{}:
	default:
	}
}

// Stop closes the stream for good.
func (k *Conn) Stop() {
	if k.stop != nil {
		k.stop()
	}
}

func (k *Conn) run(ctx context.Context) {
	for ctx.Err() == nil {
		k.mu.Lock()
		sctx, cancel := context.WithCancel(ctx)
		pane := k.pane
		k.cancel = cancel
		k.mu.Unlock()
		select { // a Retry before this stream began is for an older one
		case <-k.again:
		default:
		}
		err := k.Client.Stream(sctx, pane, func(e Event) {
			if e.Name == "hello" {
				k.Wait.Reset()
				k.Status(true, nil, time.Time{})
			}
			k.On(e)
		})
		retried := sctx.Err() != nil
		cancel()
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrUnpaired):
			k.Status(false, err, time.Time{})
			return
		case retried:
			continue // a Watch or Retry: open the new stream at once
		}
		wait := k.Wait.Next()
		k.Status(false, err, time.Now().Add(wait))
		select {
		case <-time.After(wait):
		case <-k.again:
			k.Wait.Reset()
		case <-ctx.Done():
			return
		}
	}
}
