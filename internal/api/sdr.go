package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jlambert/trackflights/internal/flight"
	"github.com/jlambert/trackflights/internal/sdr"
)

func (s *Server) ingestSDR(w http.ResponseWriter, r *http.Request) {
	var frames []sdr.Frame
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&frames); err != nil || len(frames) > 500 {
		http.Error(w, "invalid frame batch", http.StatusBadRequest)
		return
	}
	s.sdrMu.Lock()
	defer s.sdrMu.Unlock()
	for _, f := range frames {
		if s.nostrEnabled() {
			continue
		}
		if f.Received.IsZero() || !f.Received.After(s.sdrLastFrame) {
			continue
		}
		if _, err := s.flights.ConsumeLineAt(f.Frame, f.Received); err != nil {
			http.Error(w, "invalid frame", http.StatusBadRequest)
			return
		}
		s.sdrLastFrame = f.Received
	}
	switch r.Header.Get("X-SDR-Signal") {
	case "receiving", "no_signal", "receiver_unavailable":
		s.sdrSignal = r.Header.Get("X-SDR-Signal")
	default:
		s.sdrSignal = "unknown"
	}
	s.sdrLastContact = time.Now()
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) SDRLastContact() time.Time {
	s.sdrMu.Lock()
	defer s.sdrMu.Unlock()
	return s.sdrLastContact
}

// liveState reports connection freshness independently of aircraft expiry.
func (s *Server) liveState() struct {
	flight.LiveState
	SDRSignal   string `json:"sdrSignal"`
	NostrStatus string `json:"nostrStatus"`
} {
	s.sdrMu.Lock()
	status := s.sdrSignal
	nostrStatus := s.nostrStatus
	if nostrStatus == "" {
		if s.nostrEnabled() {
			nostrStatus = "connecting"
		} else {
			nostrStatus = "disabled"
		}
	}
	if s.sdrLastContact.IsZero() || time.Since(s.sdrLastContact) > 15*time.Second {
		status = "disconnected"
	}
	s.sdrMu.Unlock()
	return struct {
		flight.LiveState
		SDRSignal   string `json:"sdrSignal"`
		NostrStatus string `json:"nostrStatus"`
	}{s.flights.LiveState(), status, nostrStatus}
}
