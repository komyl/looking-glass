package report

import (
	"encoding/json"
	"sync"
	"time"
)

const (
	ephemeralTTL     = 30 * time.Minute
	ephemeralSweep   = 5 * time.Minute
	maxEphemeralSize = 2000
)

// Ephemeral is one completed check's result, held in memory only, keyed by
// the request ID handed back to the client. It is the source promote reads
// from — never data the client submits.
type Ephemeral struct {
	Kind      string
	Target    string
	Data      json.RawMessage
	CreatedAt time.Time
}

// EphemeralCache holds the last 30 minutes of completed check results,
// single-node/in-process only — there is no cross-node replication, by
// design (see docs/API.md and the whatfind.md report for this feature).
type EphemeralCache struct {
	mu      sync.Mutex
	entries map[string]Ephemeral
}

func NewEphemeralCache() *EphemeralCache {
	c := &EphemeralCache{entries: make(map[string]Ephemeral)}
	go c.sweepLoop()
	return c
}

// Put stores a completed result under id. If the cache is at capacity, the
// single oldest entry is evicted to make room — safe here because an
// evicted entry only means "Permanent Link" can no longer be pressed for
// that already-delivered result; the result itself was already returned to
// the client in the original response.
func (c *EphemeralCache) Put(id, kind, target string, data json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= maxEphemeralSize {
		var oldestID string
		var oldestAt time.Time
		first := true
		for k, v := range c.entries {
			if first || v.CreatedAt.Before(oldestAt) {
				oldestID, oldestAt, first = k, v.CreatedAt, false
			}
		}
		if oldestID != "" {
			delete(c.entries, oldestID)
		}
	}

	c.entries[id] = Ephemeral{
		Kind:      kind,
		Target:    target,
		Data:      data,
		CreatedAt: time.Now(),
	}
}

func (c *EphemeralCache) Get(id string) (Ephemeral, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	return e, ok
}

func (c *EphemeralCache) sweepLoop() {
	ticker := time.NewTicker(ephemeralSweep)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-ephemeralTTL)
		c.mu.Lock()
		for k, v := range c.entries {
			if v.CreatedAt.Before(cutoff) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}
