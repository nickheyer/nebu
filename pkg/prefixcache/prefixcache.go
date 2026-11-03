// Package prefixcache keeps an approximate index of which endpoints hold which prompt prefixes
package prefixcache

import (
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// A chained chunk hash, each hash covering its chunk and every chunk before it
type BlockHash uint64

// Index configuration
type Config struct {
	// Characters per chunk when the prompt is text
	ChunkChars int
	// Tokens per chunk when the prompt is token ids
	ChunkTokens int
	// Blocks kept per endpoint when the endpoint reports no cache size
	DefaultBlocks int
	// Cap on prompt chunks hashed per request
	MaxChunks int
	// How long an entry stays before a sweep removes it, zero keeping LRU order alone
	TTL time.Duration
	// Cap on entries across every endpoint, the least recently used endpoints losing their oldest first
	MaxEntries int
}

// Size of the index
type Stats struct {
	// Distinct hashes held by at least one endpoint
	Hashes int
	// Hash and endpoint pairs held
	Entries   int
	Endpoints int
	// Estimated heap bytes held by the index
	Bytes int64
}

// What a lookup found for an endpoint
type Match struct {
	Endpoint datalayer.EndpointID
	// Chunks matched from the start of the prompt
	Blocks int
}

// The approximate index and its lookups
type Indexer interface {
	// Hashes the prompt into chained chunk hashes, scoped by model and adapter
	Hash(model, adapter, prompt string, tokenIDs []int) []BlockHash
	// The endpoints holding the longest matching prefix, best first
	Match(hashes []BlockHash, candidates []datalayer.EndpointID) []Match
	// Records that an endpoint now holds every hash of a prompt
	Record(endpoint datalayer.EndpointID, hashes []BlockHash)
	// Removes the approximate entries of a prompt from an endpoint that never computed it, reported ones staying
	Unrecord(endpoint datalayer.EndpointID, hashes []BlockHash)
	// Replaces the capacity used to evict an endpoint's blocks
	SetCapacity(endpoint datalayer.EndpointID, blocks int)
	// Merges a reported index from the endpoint itself, which outranks the approximate one
	Report(endpoint datalayer.EndpointID, hashes []BlockHash)
	// Removes an endpoint and everything indexed for it
	Forget(endpoint datalayer.EndpointID)
	// Chunks counted over the hashes, for scoring
	Len() int
	Stats() Stats
}

// Picks the same endpoint for the same prefix on every gateway when the index knows nothing
type Ring interface {
	// The endpoint the first hash lands on among candidates, by consistent hashing with bounded load
	Pick(first BlockHash, candidates []datalayer.EndpointID, load func(datalayer.EndpointID) int, maxLoad int) (datalayer.EndpointID, bool)
}
