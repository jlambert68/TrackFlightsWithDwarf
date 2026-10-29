package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jlambert/trackflights/internal/flight"
)

func (s *Server) flightHistoryTracks(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	date := query.Get("date")
	parsed, err := time.Parse("2006-01-02", date)
	before, cursorErr := strconv.ParseInt(query.Get("before"), 10, 64)
	ids := query["icao"]
	if err != nil || parsed.Format("2006-01-02") != date || cursorErr != nil || before < 0 || len(ids) == 0 || len(ids) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid replay track request"})
		return
	}
	for i, id := range ids {
		id = strings.ToUpper(id)
		if len(id) != 6 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid aircraft identifier"})
			return
		}
		if _, err := hex.DecodeString(id); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid aircraft identifier"})
			return
		}
		ids[i] = id
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(w)
	write := func(value any) error {
		if err := encoder.Encode(value); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := write(map[string]string{"type": "header"}); err != nil {
		return
	}
	cfg := s.currentConfig()
	err = flight.StreamReplayTracks(r.Context(), cfg.Flight.TrackingDir, date, flight.Receiver{Latitude: cfg.Flight.Latitude, Longitude: cfg.Flight.Longitude}, ids, before, func(events []flight.ReplayEvent) error {
		points := make([]map[string]any, 0, len(events))
		for _, event := range events {
			a := event.Aircraft
			points = append(points, map[string]any{"icao": a.ICAO, "timestamp": a.PositionTimestamp, "latitude": a.Latitude, "longitude": a.Longitude, "altitudeFeet": a.AltitudeFeet})
		}
		return write(map[string]any{"type": "chunk", "points": points})
	}, cfg.Flight.AircraftExpiry())
	if err != nil {
		if r.Context().Err() == nil {
			s.logger.Error("stream replay tracks", "error", err, "date", date)
			_ = write(map[string]string{"type": "error", "error": "recorded tracks could not be read"})
		}
		return
	}
	_ = write(map[string]string{"type": "end"})
}
