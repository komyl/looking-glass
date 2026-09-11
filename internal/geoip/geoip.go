package geoip

import (
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
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

type csvQuoteState uint8

const (
	csvFieldStart csvQuoteState = iota
	csvUnquoted
	csvQuoted
	csvAfterQuote
)

var errBlankCSVLine = errors.New("blank CSV line")

type csvLineReader struct {
	reader      io.Reader
	lineSize    int
	lineEndsCR  bool
	state       csvQuoteState
	terminalErr error
}

var canonicalHeader = [...]string{
	"network",
	"country",
	"country_code",
	"continent",
	"continent_code",
	"asn",
	"as_name",
	"as_domain",
}

func Open(path string) (*DB, error) {
	db := &DB{}
	snap := &snapshot{asnIdx: make(map[string]*Record, 100000)}

	if err := db.loadFile(path, snap); err != nil {
		return nil, err
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

	cr := csv.NewReader(&csvLineReader{reader: r})
	cr.ReuseRecord = false

	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("header: %w", err)
	}
	if len(header) != len(canonicalHeader) {
		return fmt.Errorf("header: got %q, want %q", header, canonicalHeader)
	}
	for i, field := range canonicalHeader {
		if header[i] != field {
			return fmt.Errorf("header: got %q, want %q", header,
				canonicalHeader)
		}
	}

	for record := 2; ; record++ {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("record %d: %w", record, err)
		}
		if len(row) != len(canonicalHeader) {
			return fmt.Errorf("record %d: got %d fields, want %d", record,
				len(row), len(canonicalHeader))
		}

		prefix, err := netip.ParsePrefix(row[0])
		if err != nil {
			return fmt.Errorf("record %d: invalid network %q: %w", record,
				row[0], err)
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

		snap.insert(prefix.Masked(), rec)
		snap.count++

		if rec.ASN != "" {
			if _, exists := snap.asnIdx[rec.ASN]; !exists {
				snap.asnIdx[rec.ASN] = rec
			}
		}
	}

	log.Printf("[geoip] loaded %d prefixes from %s", snap.count, path)
	return nil
}

func (r *csvLineReader) Read(data []byte) (int, error) {
	if r.terminalErr != nil {
		return 0, r.terminalErr
	}

	n, readErr := r.reader.Read(data)
	for i, value := range data[:n] {
		if value == '\n' && r.state != csvQuoted &&
			(r.lineSize == 0 || (r.lineSize == 1 && r.lineEndsCR)) {
			r.terminalErr = errBlankCSVLine
			if readErr != nil && readErr != io.EOF {
				r.terminalErr = errors.Join(r.terminalErr, readErr)
			}
			return i, r.terminalErr
		}
		r.consume(value)
		if value == '\n' {
			r.lineSize = 0
			r.lineEndsCR = false
			continue
		}
		r.lineSize++
		r.lineEndsCR = value == '\r'
	}
	return n, readErr
}

func (r *csvLineReader) consume(value byte) {
	// Track only enough state to preserve quoted physical newlines.
	// encoding/csv remains authoritative for CSV grammar.
	if value == '\n' && r.state != csvQuoted {
		r.state = csvFieldStart
		return
	}

	switch r.state {
	case csvFieldStart:
		switch value {
		case '"':
			r.state = csvQuoted
		case ',':
		default:
			r.state = csvUnquoted
		}
	case csvUnquoted:
		if value == ',' {
			r.state = csvFieldStart
		}
	case csvQuoted:
		if value == '"' {
			r.state = csvAfterQuote
		}
	case csvAfterQuote:
		switch value {
		case '"':
			r.state = csvQuoted
		case ',':
			r.state = csvFieldStart
		case '\r':
		default:
			r.state = csvUnquoted
		}
	}
}

func (s *snapshot) insert(prefix netip.Prefix, rec *Record) {
	root, b := s.rootFor(prefix.Addr())
	node := root
	for i := 0; i < prefix.Bits(); i++ {
		bit := (b[i/8] >> (7 - uint(i%8))) & 1
		if node.children[bit] == nil {
			node.children[bit] = &trieNode{}
		}
		node = node.children[bit]
	}
	node.rec = rec
}

func (s *snapshot) lookupIP(ip netip.Addr) *Record {
	root, b := s.rootFor(ip)
	node := root
	var best *Record
	for i := 0; i < ip.BitLen() && node != nil; i++ {
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

func (s *snapshot) rootFor(ip netip.Addr) (*trieNode, []byte) {
	if ip.Is4() {
		b := ip.As4()
		return &s.root4, b[:]
	}
	b := ip.As16()
	return &s.root6, b[:]
}

func (db *DB) Lookup(ipStr string) *Record {
	snap := db.current.Load()
	if snap == nil {
		return nil
	}
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
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
