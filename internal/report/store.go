package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	reportTTL      = 24 * time.Hour
	reportSweep    = 12 * time.Minute
	maxActiveTotal = 2000
)

var ErrAtCapacity = errors.New("report: at capacity")

// CapturedAt is stored inside the JSON itself so expiry doesn't depend on
// file mtime surviving a backup/restore or a touch(1) by an operator.
type Report struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Target     string          `json:"target"`
	CapturedAt time.Time       `json:"captured_at"`
	Data       json.RawMessage `json:"data"`
}

type Store struct {
	dir    string
	active atomic.Int64
	mu     sync.Mutex // guards the check-then-increment in Promote
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("reports dir: %w", err)
	}
	s := &Store{dir: dir}
	s.active.Store(int64(s.countActive()))
	go s.sweepLoop()
	return s, nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Used at startup (to recover the counter across a restart) and by the
// sweeper (to self-heal any drift).
func (s *Store) countActive() int {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-reportTTL)
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var r Report
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		if r.CapturedAt.After(cutoff) {
			n++
		}
	}
	return n
}

// Rejects the write outright — never evicting an existing report to make
// room — once the global active cap is reached, since an existing report
// may be an active link someone is sharing right now.
func (s *Store) Promote(kind, target string, data json.RawMessage) (string, error) {
	s.mu.Lock()
	if s.active.Load() >= maxActiveTotal {
		s.mu.Unlock()
		return "", ErrAtCapacity
	}
	// Reserve the slot before releasing the lock so two concurrent
	// promotions can't both slip past the cap check above.
	s.active.Add(1)
	s.mu.Unlock()

	id, err := NewID()
	if err != nil {
		s.active.Add(-1)
		return "", err
	}

	r := Report{
		ID:         id,
		Kind:       kind,
		Target:     target,
		CapturedAt: time.Now(),
		Data:       data,
	}
	buf, err := json.Marshal(r)
	if err != nil {
		s.active.Add(-1)
		return "", err
	}

	tmp := s.path(id) + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		s.active.Add(-1)
		return "", err
	}
	if err := os.Rename(tmp, s.path(id)); err != nil {
		os.Remove(tmp)
		s.active.Add(-1)
		return "", err
	}
	return id, nil
}

// id must already be validated with ValidID by the caller before it is
// ever used to build a path.
func (s *Store) Get(id string) (*Report, bool) {
	if !ValidID(id) {
		return nil, false
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, false
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, false
	}
	if time.Since(r.CapturedAt) > reportTTL {
		return nil, false
	}
	return &r, true
}

func (s *Store) sweepLoop() {
	ticker := time.NewTicker(reportSweep)
	defer ticker.Stop()
	for range ticker.C {
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			continue
		}
		cutoff := time.Now().Add(-reportTTL)
		active := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			p := filepath.Join(s.dir, e.Name())
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var r Report
			if err := json.Unmarshal(data, &r); err != nil {
				continue
			}
			if r.CapturedAt.After(cutoff) {
				active++
			} else {
				os.Remove(p)
			}
		}
		s.active.Store(int64(active))
	}
}
