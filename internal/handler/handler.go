package handler

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"looking-glass/internal/bgp"
	"looking-glass/internal/geoip"
	"looking-glass/internal/ratelimit"
	"looking-glass/internal/validator"
)

var defaultResolvers = []string{
	"8.8.8.8",
	"8.8.4.4",
	"1.1.1.1",
	"1.0.0.1",
	"9.9.9.9",
	"149.112.112.112",
}

type ipSemEntry struct {
	ch   chan struct{}
	last atomic.Int64
}

type Handler struct {
	store     *bgp.Store
	geo       *geoip.DB
	rl        *ratelimit.Limiter
	index     []byte
	semaphore chan struct{}
	ipSem     sync.Map
	resolvers []string
}

func New(store *bgp.Store, geo *geoip.DB, rl *ratelimit.Limiter, indexHTML []byte) *Handler {
	resolvers := defaultResolvers
	if env := os.Getenv("LOOKING_GLASS_RESOLVERS"); env != "" {
		resolvers = strings.Split(env, ",")
		for i := range resolvers {
			resolvers[i] = strings.TrimSpace(resolvers[i])
		}
	}

	h := &Handler{
		store:     store,
		geo:       geo,
		rl:        rl,
		index:     indexHTML,
		semaphore: make(chan struct{}, 30),
		resolvers: resolvers,
	}
	go h.cleanupIPSem()
	return h
}

func (h *Handler) acquireIP(ip string) bool {
	v, _ := h.ipSem.LoadOrStore(ip, &ipSemEntry{ch: make(chan struct{}, 1)})
	e := v.(*ipSemEntry)
	e.last.Store(time.Now().UnixNano())
	select {
	case e.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *Handler) releaseIP(ip string) {
	if v, ok := h.ipSem.Load(ip); ok {
		e := v.(*ipSemEntry)
		select {
		case <-e.ch:
		default:
		}
	}
}

func (h *Handler) cleanupIPSem() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-30 * time.Minute).UnixNano()
		h.ipSem.Range(func(key, value any) bool {
			if value.(*ipSemEntry).last.Load() < cutoff {
				h.ipSem.Delete(key)
			}
			return true
		})
	}
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Real-IP"); v != "" {
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
			return ip.String()
		}
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		first := strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
		if ip := net.ParseIP(first); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func sseHeaders(w http.ResponseWriter) (http.Flusher, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	return f, true
}

func sseLine(w http.ResponseWriter, f http.Flusher, data string) {
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	f.Flush()
}

func sseDone(w http.ResponseWriter, f http.Flusher) {
	fmt.Fprintf(w, "data: [DONE]\n\n")
	f.Flush()
}

func sseErr(w http.ResponseWriter, f http.Flusher, msg string) {
	fmt.Fprintf(w, "data: [ERROR] %s\n\n", msg)
	f.Flush()
}
func sanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, prefix := range []string{"dial tcp ", "read tcp ", "write tcp "} {
		if strings.Contains(msg, prefix) {
			if arrow := strings.Index(msg, "->"); arrow >= 0 {
				msg = msg[arrow+2:]
			}
			break
		}
	}
	return msg
}

func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(h.index)
}

func (h *Handler) MyIP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"ip": clientIP(r)})
}

func (h *Handler) Info(w http.ResponseWriter, r *http.Request) {
	count, loadedAt := h.store.Stats()
	updated := "not loaded"
	if !loadedAt.IsZero() {
		updated = loadedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	writeJSON(w, map[string]any{
		"route_count": count,
		"bgp_updated": updated,
	})
}

func (h *Handler) Ping(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if err := validator.ValidateTarget(target); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	pinnedIP, err := validator.ValidateNotPrivate(r.Context(), target)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	probeTarget := target
	if pinnedIP != nil {
		probeTarget = pinnedIP.String()
	}
	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}
	count := 5
	if n, err := strconv.Atoi(r.URL.Query().Get("count")); err == nil && n >= 1 && n <= 20 {
		count = n
	}
	flusher, ok := sseHeaders(w)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	default:
		sseErr(w, flusher, "server busy — too many concurrent requests")
		return
	}
	ip := clientIP(r)
	if !h.acquireIP(ip) {
		sseErr(w, flusher, "you already have an active request — please wait")
		return
	}
	defer h.releaseIP(ip)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ping", "-c", strconv.Itoa(count), "-W", "2", "-i", "0.5", probeTarget)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sseErr(w, flusher, "internal error: "+err.Error())
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		sseErr(w, flusher, "ping failed to start: "+err.Error())
		return
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		select {
		case <-r.Context().Done():
			cmd.Process.Kill()
			return
		default:
		}
		sseLine(w, flusher, scanner.Text())
	}
	cmd.Wait()
	sseDone(w, flusher)
}

func (h *Handler) Traceroute(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if err := validator.ValidateTarget(target); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	pinnedIP, err := validator.ValidateNotPrivate(r.Context(), target)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	probeTarget := target
	if pinnedIP != nil {
		probeTarget = pinnedIP.String()
	}
	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}
	maxHops := 30
	if n, err := strconv.Atoi(r.URL.Query().Get("maxhops")); err == nil && n >= 5 && n <= 64 {
		maxHops = n
	}
	flusher, ok := sseHeaders(w)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	default:
		sseErr(w, flusher, "server busy — too many concurrent requests")
		return
	}
	ip := clientIP(r)
	if !h.acquireIP(ip) {
		sseErr(w, flusher, "you already have an active request — please wait")
		return
	}
	defer h.releaseIP(ip)
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "traceroute", "-n", "-w", "2", "-m", strconv.Itoa(maxHops), probeTarget)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sseErr(w, flusher, "internal error: "+err.Error())
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		sseErr(w, flusher, "traceroute not available: "+err.Error())
		return
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		select {
		case <-r.Context().Done():
			cmd.Process.Kill()
			return
		default:
		}
		sseLine(w, flusher, scanner.Text())
	}
	cmd.Wait()
	sseDone(w, flusher)
}

func (h *Handler) Dig(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if err := validator.ValidateTarget(target); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}
	qtype := r.URL.Query().Get("qtype")
	switch qtype {
	case "A", "AAAA", "MX", "NS", "TXT", "CNAME", "SOA", "PTR":
	default:
		qtype = "A"
	}

	debug := r.URL.Query().Get("debug") == "1"

	flusher, ok := sseHeaders(w)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	default:
		sseErr(w, flusher, "server busy — too many concurrent requests")
		return
	}
	ip := clientIP(r)
	if !h.acquireIP(ip) {
		sseErr(w, flusher, "you already have an active request — please wait")
		return
	}
	defer h.releaseIP(ip)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	const maxConcurrent = 8
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex
	answers := make(map[string]int)
	successCount := 0
	total := len(h.resolvers)

	for _, resolver := range h.resolvers {
		wg.Add(1)
		go func(resolver string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			cmd := exec.CommandContext(cmdCtx, "dig", "@"+resolver, "+noall", "+answer", "+comments", "+timeout=4", target, qtype)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				if debug {
					sseLine(w, flusher, fmt.Sprintf("[Resolver %s] internal error", resolver))
				}
				return
			}
			cmd.Stderr = cmd.Stdout

			if err := cmd.Start(); err != nil {
				if debug {
					sseLine(w, flusher, fmt.Sprintf("[Resolver %s] dig failed to start", resolver))
				}
				return
			}

			hasAnswer := false
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "ANSWER SECTION") {
					hasAnswer = true
					continue
				}
				if hasAnswer && strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), ";") {
					parts := strings.Fields(line)
					if len(parts) >= 5 {
						normalized := parts[0] + " IN " + parts[3] + " " + strings.Join(parts[4:], " ")
						mu.Lock()
						answers[normalized]++
						mu.Unlock()
					}
				}
			}
			cmd.Wait()

			if hasAnswer {
				mu.Lock()
				successCount++
				mu.Unlock()
			} else if debug {
				sseLine(w, flusher, fmt.Sprintf("[Resolver %s] NOERROR (no record) or SERVFAIL/timeout", resolver))
			}
		}(resolver)
	}

	wg.Wait()

	if len(answers) == 0 {
		sseLine(w, flusher, "No record found on any resolver")
	} else {
		for record, count := range answers {
			sseLine(w, flusher, fmt.Sprintf("%s   (found on %d resolvers)", record, count))
		}
	}

	summary := fmt.Sprintf("=== Summary ===\nRecord found on %d out of %d resolvers", successCount, total)
	sseLine(w, flusher, summary)
	sseDone(w, flusher)
}

func (h *Handler) BGP(w http.ResponseWriter, r *http.Request) {
	qtype := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		writeError(w, "query parameter is required", http.StatusBadRequest)
		return
	}
	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}
	var routes []bgp.Route
	var lookupErr error
	switch qtype {
	case "ip":
		if err := validator.ValidateTarget(query); err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		routes, lookupErr = h.store.LookupIP(query)
	case "prefix":
		if err := validator.ValidatePrefix(query); err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		routes, lookupErr = h.store.LookupPrefix(query)
	case "asn":
		asn, err := validator.ValidateASN(query)
		if err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}
		routes = h.store.LookupASN(asn)
	default:
		writeError(w, "type must be ip, prefix, or asn", http.StatusBadRequest)
		return
	}
	if lookupErr != nil {
		writeError(w, lookupErr.Error(), http.StatusBadRequest)
		return
	}
	if routes == nil {
		routes = []bgp.Route{}
	}
	type enrichedRoute struct {
		bgp.Route
		Geo *geoip.Record `json:"geo,omitempty"`
	}
	enriched := make([]enrichedRoute, len(routes))
	for i, r := range routes {
		er := enrichedRoute{Route: r}
		if h.geo != nil && qtype == "ip" {
			er.Geo = h.geo.Lookup(query)
		}
		enriched[i] = er
	}
	var aspathEnriched []struct {
		ASN    int    `json:"asn"`
		Name   string `json:"name,omitempty"`
		Domain string `json:"domain,omitempty"`
	}
	if h.geo != nil && len(routes) > 0 {
		for _, asn := range routes[0].ASPath {
			info := struct {
				ASN    int    `json:"asn"`
				Name   string `json:"name,omitempty"`
				Domain string `json:"domain,omitempty"`
			}{ASN: asn}
			if rec := h.geo.LookupASN(asn); rec != nil {
				info.Name = rec.ASName
				info.Domain = rec.ASDomain
			}
			aspathEnriched = append(aspathEnriched, info)
		}
	}
	writeJSON(w, map[string]any{
		"count":           len(enriched),
		"routes":          enriched,
		"aspath_enriched": aspathEnriched,
	})
}
func (h *Handler) IPInfo(w http.ResponseWriter, r *http.Request) {
	targets := r.URL.Query().Get("targets")
	if targets == "" {
		writeError(w, "targets is required", http.StatusBadRequest)
		return
	}
	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}
	if h.geo == nil {
		writeJSON(w, map[string]any{})
		return
	}

	const maxTargets = 50
	result := make(map[string]any)
	count := 0
	for _, t := range strings.Split(targets, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		count++
		if count > maxTargets {
			break
		}

		rec := h.geo.Lookup(t)

		if rec == nil || rec.ASN == "" {
			routes, _ := h.store.LookupIP(t)
			if len(routes) > 0 {
				route := routes[0]
				if len(route.ASPath) > 0 {
					origin := route.ASPath[len(route.ASPath)-1]
					if asRec := h.geo.LookupASN(origin); asRec != nil {
						result[t] = map[string]string{
							"asn":    fmt.Sprintf("AS%d", origin),
							"name":   asRec.ASName,
							"domain": asRec.ASDomain,
						}
						continue
					}
				}
			}
		}

		if rec != nil {
			result[t] = map[string]string{
				"asn":    rec.ASN,
				"name":   rec.ASName,
				"domain": rec.ASDomain,
			}
		}
	}
	writeJSON(w, result)
}

func (h *Handler) SSLCheck(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if err := validator.ValidateTarget(target); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	portStr := r.URL.Query().Get("port")
	port := "443"
	if portStr != "" {
		n, err := strconv.Atoi(portStr)
		if err != nil || n < 1 || n > 65535 {
			writeError(w, "invalid port", http.StatusBadRequest)
			return
		}
		port = portStr
	}
	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}
	host := target
	pinnedIP, err := validator.ValidateNotPrivate(r.Context(), host)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	dialHost := host
	if pinnedIP != nil {
		dialHost = pinnedIP.String()
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	addr := net.JoinHostPort(dialHost, port)
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
	})
	type result struct {
		Valid     bool     `json:"valid"`
		Error     string   `json:"error,omitempty"`
		Subject   string   `json:"subject,omitempty"`
		Issuer    string   `json:"issuer,omitempty"`
		NotBefore string   `json:"not_before,omitempty"`
		NotAfter  string   `json:"not_after,omitempty"`
		DaysLeft  int      `json:"days_left,omitempty"`
		SANs      []string `json:"sans,omitempty"`
	}
	extractCert := func(conn *tls.Conn) result {
		certs := conn.ConnectionState().PeerCertificates
		if len(certs) == 0 {
			return result{Valid: false, Error: "no certificate returned"}
		}
		cert := certs[0]
		now := time.Now()
		return result{
			Valid:     err == nil,
			Subject:   cert.Subject.CommonName,
			Issuer:    cert.Issuer.CommonName,
			NotBefore: cert.NotBefore.UTC().Format("2006-01-02 15:04:05 UTC"),
			NotAfter:  cert.NotAfter.UTC().Format("2006-01-02 15:04:05 UTC"),
			DaysLeft:  int(cert.NotAfter.Sub(now).Hours() / 24),
			SANs:      cert.DNSNames,
		}
	}
	if err != nil {
		conn2, err2 := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         host,
		})
		if err2 != nil {
			writeJSON(w, result{Valid: false, Error: sanitizeErr(err)})
			return
		}
		defer conn2.Close()
		res := extractCert(conn2)
		res.Valid = false
		res.Error = sanitizeErr(err)
		writeJSON(w, res)
		return
	}
	defer conn.Close()
	writeJSON(w, extractCert(conn))
}
