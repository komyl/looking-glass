package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"looking-glass/internal/nodes"
)

func (h *Handler) Nodes(w http.ResponseWriter, r *http.Request) {
	type publicNode struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Location string `json:"location"`
		ISP      string `json:"isp"`
	}
	var pub []publicNode
	for _, n := range nodes.List {
		pub = append(pub, publicNode{
			ID:       n.ID,
			Name:     n.Name,
			Location: n.Location,
			ISP:      n.ISP,
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

	if target == "" || strings.ContainsAny(target, ";&|$(){}[]<>'\"`\n\r\t\\") {
		writeError(w, "invalid target", http.StatusBadRequest)
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	params := r.URL.Query()
	agentURL := fmt.Sprintf("%s/%s?%s", node.URL, action, params.Encode())

	req, err := http.NewRequestWithContext(r.Context(), "GET", agentURL, nil)
	if err != nil {
		fmt.Fprintf(w, "data: [ERROR] failed to create request: %s\n\n", err.Error())
		flusher.Flush()
		return
	}
	req.Header.Set("X-Agent-Secret", nodes.Secret)

	client := &http.Client{Timeout: 130 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(w, "data: [ERROR] agent unreachable: %s\n\n", err.Error())
		flusher.Flush()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(w, "data: [ERROR] agent error: %s\n\n", strings.TrimSpace(string(body)))
		flusher.Flush()
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

	if target == "" || strings.ContainsAny(target, ";&|$(){}[]<>'\"`\n\r\t\\") {
		writeError(w, "invalid target", http.StatusBadRequest)
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

	agentURL := fmt.Sprintf("%s/portcheck?target=%s&port=%s",
		node.URL,
		target,
		portStr,
	)

	req, err := http.NewRequestWithContext(r.Context(), "GET", agentURL, nil)
	if err != nil {
		writeError(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-Agent-Secret", nodes.Secret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, "agent unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.Copy(w, resp.Body)
}
