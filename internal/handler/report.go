package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"looking-glass/internal/report"
)

// Gated by a dedicated per-IP limiter (10/hour) independent of every other
// rate-limiting layer in this codebase — see docs/ARCHITECTURE.md for why
// this needed its own dimension rather than reusing the subprocess
// semaphore or the general token bucket: it's a disk write, not a probe
// execution.
func (h *Handler) Promote(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil || h.ephemeral == nil {
		writeError(w, "permanent links are unavailable", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		RequestID string `json:"request_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !report.ValidID(body.RequestID) {
		writeError(w, "invalid request id", http.StatusBadRequest)
		return
	}

	if !h.promoteRL.Allow(clientIP(r)) {
		writeError(w, "too many permanent links requested — try again later", http.StatusTooManyRequests)
		return
	}

	entry, ok := h.ephemeral.Get(body.RequestID)
	if !ok {
		writeError(w, "this result is no longer available to make permanent — please re-run the check", http.StatusNotFound)
		return
	}

	id, err := h.reports.Promote(entry.Kind, entry.Target, entry.Data)
	if err != nil {
		if errors.Is(err, report.ErrAtCapacity) {
			writeError(w, "too many active shared links right now — please try again later", http.StatusServiceUnavailable)
			return
		}
		writeError(w, "failed to save permanent link", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"id": id})
}

func (h *Handler) ReportRead(w http.ResponseWriter, r *http.Request) {
	if h.reports == nil {
		writeError(w, "permanent links are unavailable", http.StatusServiceUnavailable)
		return
	}
	if !h.rl.Allow(clientIP(r)) {
		writeError(w, "rate limited — try again in a minute", http.StatusTooManyRequests)
		return
	}
	id := r.URL.Query().Get("id")
	rep, ok := h.reports.Get(id)
	if !ok {
		writeError(w, "report not found or expired", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"id":          rep.ID,
		"kind":        rep.Kind,
		"target":      rep.Target,
		"captured_at": rep.CapturedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"data":        rep.Data,
	})
}
