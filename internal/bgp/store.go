package bgp

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"
)

type Route struct {
	Prefix      string   `json:"prefix"`
	NextHop     string   `json:"nexthop"`
	ASPath      []int    `json:"aspath"`
	Origin      string   `json:"origin"`
	MED         int      `json:"med"`
	LocalPref   int      `json:"localpref"`
	Communities []string `json:"communities"`
}

type bgpFile struct {
	Timestamp int64   `json:"timestamp"`
	Routes    []Route `json:"routes"`
}

type snapshot struct {
	trie       Trie
	byASN      map[int][]Route
	routeCount int
	loadedAt   time.Time
	fileMtime  time.Time
}

type Store struct {
	filepath string
	current  atomic.Pointer[snapshot]
}

func NewStore(filepath string) *Store {
	s := &Store{filepath: filepath}
	if err := s.load(); err != nil {
		log.Printf("[bgp] initial load skipped (%v) — will retry every 5 min", err)
	}
	return s
}

func (s *Store) Start() {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			info, err := os.Stat(s.filepath)
			if err != nil {
				continue
			}
			cur := s.current.Load()
			if cur == nil || info.ModTime().After(cur.fileMtime) {
				if err := s.load(); err != nil {
					log.Printf("[bgp] reload error: %v", err)
				}
			}
		}
	}()
}

func (s *Store) load() error {
	f, err := os.Open(s.filepath)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	var data bgpFile
	dec := json.NewDecoder(f)
	if err := dec.Decode(&data); err != nil {
		return fmt.Errorf("JSON decode: %w", err)
	}

	snap := &snapshot{
		byASN:     make(map[int][]Route, 1024),
		loadedAt:  time.Now(),
		fileMtime: info.ModTime(),
	}

	skipped := 0
	for _, r := range data.Routes {
		_, ipnet, err := net.ParseCIDR(r.Prefix)
		if err != nil {
			skipped++
			continue
		}
		snap.trie.Insert(ipnet, r)
		for _, asn := range r.ASPath {
			snap.byASN[asn] = append(snap.byASN[asn], r)
		}
		snap.routeCount++
	}

	s.current.Store(snap)
	log.Printf("[bgp] loaded %d routes (%d skipped) from %s", snap.routeCount, skipped, s.filepath)
	return nil
}

func (s *Store) LookupIP(ipStr string) ([]Route, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP: %q", ipStr)
	}
	snap := s.current.Load()
	if snap == nil {
		return nil, nil
	}
	routes := snap.trie.LookupIP(ip)
	for _, r := range routes {
		if r.Prefix == "0.0.0.0/0" || r.Prefix == "::/0" {
			return nil, nil
		}
	}
	return routes, nil
}

func (s *Store) LookupPrefix(cidr string) ([]Route, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}
	snap := s.current.Load()
	if snap == nil {
		return nil, nil
	}
	return snap.trie.LookupExactPrefix(ipnet), nil
}

func (s *Store) LookupASN(asn int) []Route {
	snap := s.current.Load()
	if snap == nil {
		return nil
	}
	routes := snap.byASN[asn]
	if len(routes) > 1000 {
		return routes[:1000]
	}
	return routes
}

func (s *Store) Stats() (count int, loadedAt time.Time) {
	snap := s.current.Load()
	if snap == nil {
		return 0, time.Time{}
	}
	return snap.routeCount, snap.loadedAt
}
