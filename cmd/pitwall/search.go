//go:build !nogui

package main

import "github.com/quanticstudios/pitwall/internal/proto"

// SearchResults hands the palette the batches received since its last frame.
func (b *backend) SearchResults() []proto.SearchAllResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.all
	b.all = nil
	return r
}

// ChatSuggestion implements app.Suggester: chat id's latest answer to
// ChatSuggest.
func (b *backend) ChatSuggestion(id string) (proto.ChatSuggestResult, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.suggests[id]
	return r, ok
}

// ServerStops implements app.ServerStopper: the answers to tab ws's
// ServerStops that came since the last call.
func (b *backend) ServerStops(ws string) []proto.ServerStopResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.stops[ws]
	delete(b.stops, ws)
	return r
}

// ChatAsides implements app.ChatAsker: the answers to chat id's side
// questions that came since the last call.
func (b *backend) ChatAsides(id string) []proto.ChatAsideResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.asides[id]
	delete(b.asides, id)
	return r
}

// RewindChecks implements app.RewindChecker: the daemon's answers to chat
// id's ChatRewindCheck that came since the last call.
func (b *backend) RewindChecks(id string) []proto.ChatRewindFiles {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.rewinds[id]
	delete(b.rewinds, id)
	return r
}

// ChatFound implements app.ChatFinder: the newest find result for chat id
// that came since the last call.
func (b *backend) ChatFound(id string) (proto.ChatFindResult, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.finds[id]
	delete(b.finds, id)
	return r, ok
}
