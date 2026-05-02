package ratelimit

import (
	"sync"
	"time"
)

type entry struct {
	mu        sync.Mutex
	tokens    float64
	lastCheck time.Time
}

type Limiter struct {
	mu      sync.RWMutex
	entries map[string]*entry
	rate    float64
	maxTok  float64
}

func New(rpm int, burst int) *Limiter {
	l := &Limiter{
		entries: make(map[string]*entry),
		rate:    float64(rpm) / 60.0,
		maxTok:  float64(burst),
	}
	go l.cleanup()
	return l
}

func (l *Limiter) Allow(key string) bool {
	l.mu.RLock()
	e, ok := l.entries[key]
	l.mu.RUnlock()

	if !ok {
		l.mu.Lock()
		if e, ok = l.entries[key]; !ok {
			e = &entry{tokens: l.maxTok - 1, lastCheck: time.Now()}
			l.entries[key] = e
			l.mu.Unlock()
			return true
		}
		l.mu.Unlock()
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(e.lastCheck).Seconds()
	e.tokens = min(l.maxTok, e.tokens+elapsed*l.rate)
	e.lastCheck = now

	if e.tokens >= 1.0 {
		e.tokens -= 1.0
		return true
	}
	return false
}

func (l *Limiter) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		for key, e := range l.entries {
			e.mu.Lock()
			stale := time.Since(e.lastCheck) > 30*time.Minute
			e.mu.Unlock()
			if stale {
				delete(l.entries, key)
			}
		}
		l.mu.Unlock()
	}
}
