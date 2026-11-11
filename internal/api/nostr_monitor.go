package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// nostrMessagesStream subscribes without a kind or author filter; the URL is
// chosen from server settings rather than accepting arbitrary client URLs.
func (s *Server) nostrMessagesStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	choice := r.URL.Query().Get("relay")
	if choice != "" && choice != "analysis" && choice != "flight" {
		http.Error(w, "invalid relay", 400)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(kind string, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
		if err == nil {
			flusher.Flush()
		}
		return err
	}
	if write("status", map[string]string{"state": "connecting"}) != nil {
		return
	}
	cfg := s.currentConfig()
	var relay *nostr.Relay
	var err error
	if choice == "flight" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		relay, err = nostr.RelayConnect(ctx, cfg.Flight.NostrRelayURL)
		cancel()
	} else {
		relay, err = s.connectTrackingRelay(r.Context(), cfg.Tracking)
	}
	if err != nil {
		_ = write("status", map[string]string{"state": "disconnected", "error": err.Error()})
		return
	}
	defer relay.Close()
	sub, err := relay.Subscribe(r.Context(), nostr.Filters{{Limit: 100}})
	if err != nil {
		_ = write("status", map[string]string{"state": "disconnected", "error": err.Error()})
		return
	}
	defer sub.Unsub()
	if write("status", map[string]string{"state": "connected"}) != nil {
		return
	}
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-relay.Context().Done():
			_ = write("status", map[string]string{"state": "disconnected"})
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-sub.Events:
			if !ok {
				return
			}
			if event != nil {
				if err := write("nostr", event); err != nil {
					return
				}
			}
		}
	}
}
