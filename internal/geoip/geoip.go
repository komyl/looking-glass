package geoip

import (
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

type Record struct {
	Country       string `json:"country,omitempty"`
	CountryCode   string `json:"country_code,omitempty"`
	Continent     string `json:"continent,omitempty"`
	ContinentCode string `json:"continent_code,omitempty"`
	ASN           string `json:"asn,omitempty"`
	ASName        string `json:"as_name,omitempty"`
	ASDomain      string `json:"as_domain,omitempty"`
}

type trieNode struct {
	children [2]*trieNode
	rec      *Record
}

type snapshot struct {
	root4  trieNode
	root6  trieNode
	asnIdx map[string]*Record
	count  int
}

type DB struct {
	current atomic.Pointer[snapshot]
}

func Open(paths ...string) (*DB, error) {
	db := &DB{}
	snap := &snapshot{asnIdx: make(map[string]*Record, 100000)}

	for _, path := range paths {
		if err := db.loadFile(path, snap); err != nil {
			return nil, err
		}
	}

	db.current.Store(snap)
	return db, nil
}

func (db *DB) loadFile(path string, snap *snapshot) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		r = gz
	}

	cr := csv.NewReader(r)
	cr.ReuseRecord = false

	if _, err := cr.Read(); err != nil {
		return fmt.Errorf("header: %w", err)
	}

	skipped := 0
	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			skipped++
			continue
		}
		if len(row) < 8 {
			skipped++
			continue
		}

		_, ipnet, err := net.ParseCIDR(row[0])
		if err != nil {
			skipped++
			continue
		}

		rec := &Record{
			Country:       row[1],
			CountryCode:   row[2],
			Continent:     row[3],
			ContinentCode: row[4],
			ASN:           row[5],
			ASName:        row[6],
			ASDomain:      row[7],
		}

		if existing := snap.lookupIP(ipnet.IP); existing != nil {
    existing.merge(rec)
} else {
    snap.insert(ipnet, rec)
    snap.count++
}

		if rec.ASN != "" {
			if _, exists := snap.asnIdx[rec.ASN]; !exists {
				snap.asnIdx[rec.ASN] = rec
			}
		}
	}

	log.Printf("[geoip] loaded from %s (skipped %d)", path, skipped)
	return nil
}

func (s *snapshot) insert(ipnet *net.IPNet, rec *Record) {
	ones, _ := ipnet.Mask.Size()
	root, b := s.rootFor(ipnet.IP)
	node := root
	for i := 0; i < ones; i++ {
		bit := (b[i/8] >> (7 - uint(i%8))) & 1
		if node.children[bit] == nil {
			node.children[bit] = &trieNode{}
		}
		node = node.children[bit]
	}
	node.rec = rec
}

func (s *snapshot) lookupIP(ip net.IP) *Record {
	root, b, bits := s.rootForIP(ip)
	node := root
	var best *Record
	for i := 0; i < bits && node != nil; i++ {
		if node.rec != nil {
			best = node.rec
		}
		bit := (b[i/8] >> (7 - uint(i%8))) & 1
		node = node.children[bit]
	}
	if node != nil && node.rec != nil {
		best = node.rec
	}
	return best
}

func (s *snapshot) rootFor(ip net.IP) (*trieNode, []byte) {
	if v4 := ip.To4(); v4 != nil {
		return &s.root4, v4
	}
	return &s.root6, ip.To16()
}

func (s *snapshot) rootForIP(ip net.IP) (*trieNode, []byte, int) {
	if v4 := ip.To4(); v4 != nil {
		return &s.root4, v4, 32
	}
	b := ip.To16()
	return &s.root6, b, 128
}

func (db *DB) Lookup(ipStr string) *Record {
	snap := db.current.Load()
	if snap == nil {
		return nil
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil
	}
	return snap.lookupIP(ip)
}

func (db *DB) LookupASN(asn int) *Record {
	snap := db.current.Load()
	if snap == nil {
		return nil
	}
	key := "AS" + strconv.Itoa(asn)
	return snap.asnIdx[key]
}

func CountryFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	r1 := rune(code[0]-'A') + 0x1F1E6
	r2 := rune(code[1]-'A') + 0x1F1E6
	return string([]rune{r1, r2})
}

func (r *Record) merge(other *Record) {
	if other.Country != "" {
		r.Country = other.Country
	}
	if other.CountryCode != "" {
		r.CountryCode = other.CountryCode
	}
	if other.Continent != "" {
		r.Continent = other.Continent
	}
	if other.ContinentCode != "" {
		r.ContinentCode = other.ContinentCode
	}
	if other.ASN != "" {
		r.ASN = other.ASN
	}
	if other.ASName != "" {
		r.ASName = other.ASName
	}
	if other.ASDomain != "" {
		r.ASDomain = other.ASDomain
	}
}