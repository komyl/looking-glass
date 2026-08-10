package nodes

import (
	"io"
	"net/http"
	"sync"
	"time"
)

// Health-check tuning: 12s interval keeps a dead agent's exclusion window
// short without hammering every node; 2 consecutive misses before marking
// dead absorbs a single dropped probe, 1 hit to recover keeps a returning
// agent useful again quickly.
const (
	healthCheckInterval = 12 * time.Second
	healthCheckTimeout  = 3 * time.Second
	deadAfterMisses     = 2
)

// Dedicated client for health checks — never shared with Proxy, PortCheck,
// or PingAll, so a slow agent here can't add latency to a real request.
var healthClient = &http.Client{Timeout: healthCheckTimeout}

// Liveness is tracked here rather than as a field on Node: PingAll ranges
// over List and passes Node by value into per-node goroutines
// (`go func(idx int, node nodes.Node)`), so a mutable field on Node would
// be copied at fan-out time and never observe later updates from the
// checker.
type liveness struct {
	mu     sync.RWMutex
	live   map[string]bool
	misses map[string]int
}

var lv = newLiveness()

func newLiveness() *liveness {
	l := &liveness{
		live:   make(map[string]bool),
		misses: make(map[string]int),
	}
	// Optimistic at startup: treating every node as dead until the first
	// check round completes would empty every node list for the first
	// healthCheckInterval after every restart.
	for _, n := range List {
		l.live[n.ID] = true
	}
	return l
}

// StartHealthChecker runs a background goroutine for the life of the
// process, polling every agent's /health endpoint on a fixed ticker.
// It is never triggered by an incoming user request.
func StartHealthChecker() {
	go func() {
		checkAll()
		ticker := time.NewTicker(healthCheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			checkAll()
		}
	}()
}

func checkAll() {
	var wg sync.WaitGroup
	for _, n := range List {
		wg.Add(1)
		go func(node Node) {
			defer wg.Done()
			checkOne(node)
		}(n)
	}
	wg.Wait()
}

func checkOne(n Node) {
	req, err := http.NewRequest(http.MethodGet, n.URL+"/health", nil)
	if err != nil {
		lv.recordFailure(n.ID)
		return
	}
	req.Header.Set("X-Agent-Secret", Secret)

	resp, err := healthClient.Do(req)
	if err != nil {
		lv.recordFailure(n.ID)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		lv.recordFailure(n.ID)
		return
	}
	lv.recordSuccess(n.ID)
}

func (l *liveness) recordFailure(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.misses[id]++
	if l.misses[id] >= deadAfterMisses {
		l.live[id] = false
	}
}

func (l *liveness) recordSuccess(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.misses[id] = 0
	l.live[id] = true
}

// IsLive reports whether node id passed its most recent health check.
func IsLive(id string) bool {
	lv.mu.RLock()
	defer lv.mu.RUnlock()
	return lv.live[id]
}

// LiveNodes returns the currently-reachable nodes, in the same order as
// List.
func LiveNodes() []Node {
	lv.mu.RLock()
	defer lv.mu.RUnlock()
	out := make([]Node, 0, len(List))
	for _, n := range List {
		if lv.live[n.ID] {
			out = append(out, n)
		}
	}
	return out
}
