package api

import (
	"encoding/json"
	"net/http"
	"time"

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
		if f.Received.IsZero() || !f.Received.After(s.sdrLastFrame) {
			continue
		}
		if _, err := s.flights.ConsumeLineAt(f.Frame, f.Received); err != nil {
			http.Error(w, "invalid frame", http.StatusBadRequest)
			return
		}
		s.sdrLastFrame = f.Received
	}
	s.sdrLastContact = time.Now()
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) SDRLastContact() time.Time {
	s.sdrMu.Lock()
	defer s.sdrMu.Unlock()
	return s.sdrLastContact
}
