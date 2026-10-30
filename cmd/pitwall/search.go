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
