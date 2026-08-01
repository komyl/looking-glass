package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"looking-glass/internal/nodes"
	"looking-glass/internal/validator"
)

func (h *Handler) Nodes(w http.ResponseWriter, r *http.Request) {
	type publicNode struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Location string `json:"location"`
	}
	var pub []publicNode
	for _, n := range nodes.List {
		pub = append(pub, publicNode{
			ID:       n.ID,
			Name:     n.Name,
			Location: n.Location,
		})
	}
	writeJSON(w, pub)
}

func (h *Handler) Proxy(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node")
	action := r.URL.Query().Get("action")
	target := r.URL.Query().Get("target")
	target = strings.TrimPrefix(target, "https://")
	target = strings.TrimPrefix(target, "http://")
	target = strings.TrimSuffix(target, "/")
	target = strings.Split(target, "/")[0]

	if err := validator.ValidateTarget(target); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch action {
	case "ping", "traceroute", "portcheck":
	default:
		writeError(w, "action must be ping, traceroute, or portcheck", http.StatusBadRequest)
		return
	}

	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}

	var node *nodes.Node
	for i := range nodes.List {
		if nodes.List[i].ID == nodeID {
			node = &nodes.List[i]
			break
		}
	}
	if node == nil {
		writeError(w, "unknown node", http.StatusBadRequest)
		return
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

	q := url.Values{"target": {target}}
	switch action {
	case "ping":
		if v := r.URL.Query().Get("count"); v != "" {
			q.Set("count", v)
		}
	case "traceroute":
		if v := r.URL.Query().Get("maxhops"); v != "" {
			q.Set("maxhops", v)
		}
	case "portcheck":
		if v := r.URL.Query().Get("port"); v != "" {
			q.Set("port", v)
		}
	}
	agentURL := fmt.Sprintf("%s/%s?%s", node.URL, action, q.Encode())

	req, err := http.NewRequestWithContext(r.Context(), "GET", agentURL, nil)
	if err != nil {
		sseErr(w, flusher, "failed to create request: "+sanitizeErr(err))
		return
	}
	req.Header.Set("X-Agent-Secret", nodes.Secret)

	client := &http.Client{Timeout: 130 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		sseErr(w, flusher, "agent unreachable")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		sseErr(w, flusher, "agent error: "+strings.TrimSpace(string(body)))
		return
	}

	buf := make([]byte, 4096)
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}
}

func writeJSONNodes(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
func (h *Handler) PortCheck(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node")
	target := r.URL.Query().Get("target")
	portStr := r.URL.Query().Get("port")

	if err := validator.ValidateTarget(target); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		writeError(w, "invalid port", http.StatusBadRequest)
		return
	}

	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}

	var node *nodes.Node
	for i := range nodes.List {
		if nodes.List[i].ID == nodeID {
			node = &nodes.List[i]
			break
		}
	}
	if node == nil {
		writeError(w, "unknown node", http.StatusBadRequest)
		return
	}

	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	default:
		writeError(w, "server busy — too many concurrent requests", http.StatusTooManyRequests)
		return
	}
	ip := clientIP(r)
	if !h.acquireIP(ip) {
		writeError(w, "you already have an active request — please wait", http.StatusTooManyRequests)
		return
	}
	defer h.releaseIP(ip)

	q := url.Values{"target": {target}, "port": {portStr}}
	agentURL := fmt.Sprintf("%s/portcheck?%s", node.URL, q.Encode())

	req, err := http.NewRequestWithContext(r.Context(), "GET", agentURL, nil)
	if err != nil {
		writeError(w, "internal error: "+sanitizeErr(err), http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-Agent-Secret", nodes.Secret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, "agent unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.Copy(w, resp.Body)
}
func (h *Handler) PingAll(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if err := validator.ValidateTarget(target); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}

	type nodeResult struct {
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		Sent     int     `json:"sent"`
		Received int     `json:"received"`
		Loss     float64 `json:"loss"`
		RTTMin   float64 `json:"rtt_min"`
		RTTAvg   float64 `json:"rtt_avg"`
		RTTMax   float64 `json:"rtt_max"`
		Error    string  `json:"error,omitempty"`
		Status   string  `json:"status"`
	}

	results := make([]nodeResult, len(nodes.List))
	for i, n := range nodes.List {
		results[i] = nodeResult{ID: n.ID, Name: n.Name, Status: "pending"}
	}

	type agentResp struct {
		Sent     int     `json:"sent"`
		Received int     `json:"received"`
		Loss     float64 `json:"loss"`
		RTTMin   float64 `json:"rtt_min"`
		RTTAvg   float64 `json:"rtt_avg"`
		RTTMax   float64 `json:"rtt_max"`
		Error    string  `json:"error,omitempty"`
	}

	const maxConcurrentAgents = 8
	sem := make(chan struct{}, maxConcurrentAgents)
	var wg sync.WaitGroup
	for i, n := range nodes.List {
		wg.Add(1)
		go func(idx int, node nodes.Node) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			agentURL := fmt.Sprintf("%s/ping-summary?target=%s&count=4", node.URL, target)
			req, err := http.NewRequest("GET", agentURL, nil)
			if err != nil {
				results[idx].Status = "error"
				results[idx].Error = err.Error()
				return
			}
			req.Header.Set("X-Agent-Secret", nodes.Secret)

			client := &http.Client{Timeout: 35 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				results[idx].Status = "error"
				results[idx].Error = "agent unreachable"
				return
			}
			defer resp.Body.Close()

			var ar agentResp
			if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
				results[idx].Status = "error"
				results[idx].Error = "invalid response"
				return
			}

			results[idx].Sent = ar.Sent
			results[idx].Received = ar.Received
			results[idx].Loss = ar.Loss
			results[idx].RTTMin = ar.RTTMin
			results[idx].RTTAvg = ar.RTTAvg
			results[idx].RTTMax = ar.RTTMax
			results[idx].Error = ar.Error

			if ar.Error != "" {
				results[idx].Status = "error"
			} else if ar.Loss == 100 {
				results[idx].Status = "down"
			} else if ar.Loss > 0 {
				results[idx].Status = "degraded"
			} else {
				results[idx].Status = "ok"
			}
		}(i, n)
	}

	wg.Wait()
	writeJSON(w, map[string]any{"target": target, "results": results})
}
