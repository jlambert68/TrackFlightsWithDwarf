package api

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jlambert/trackflights/internal/config"
	"github.com/jlambert/trackflights/internal/flight"
	"github.com/jlambert/trackflights/internal/flight/aircraft"
	"github.com/jlambert/trackflights/internal/flight/flighthistory"
	"github.com/jlambert/trackflights/internal/flight/flightroute"
	"github.com/jlambert/trackflights/internal/jobs"
)

//go:embed static/*
var assets embed.FS

type Server struct {
	logger         *slog.Logger
	config         config.Config
	configMu       sync.RWMutex
	store          *config.Store
	jobs           *jobs.Manager
	flights        *flight.Service
	history        *flighthistory.Client
	historyMu      sync.Mutex
	metadata       *aircraft.Client
	routes         *flightroute.Client
	dataRoot       string
	started        time.Time
	bellMu         sync.Mutex
	lastBell       time.Time
	lastBellFailed bool
	playBell       func(context.Context) error
}

func NewServer(logger *slog.Logger, cfg config.Config, store *config.Store, manager *jobs.Manager, flights *flight.Service, dataRoot string) *Server {
	flights.SetAircraftExpiry(cfg.Flight.AircraftExpiry())
	return &Server{logger: logger, config: cfg, store: store, jobs: manager, flights: flights, history: flighthistory.NewClient("https://adsb.lol"), metadata: aircraft.NewClient("https://hexdb.io/api/v1/aircraft"), routes: flightroute.NewClient("https://api.adsbdb.com/v0/callsign"), dataRoot: dataRoot, started: time.Now().UTC(), playBell: playHostBell}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/settings", s.settings)
	mux.HandleFunc("PUT /api/v1/settings", s.updateSettings)
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/v1/jobs/demo", s.startDemoJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("GET /api/v1/events/stream", s.events)
	mux.HandleFunc("GET /api/v1/flights/live", s.liveFlights)
	mux.HandleFunc("GET /api/v1/flights/stream", s.flightStream)
	mux.HandleFunc("POST /api/v1/flights/frames", s.ingestFlightFrame)
	mux.HandleFunc("GET /api/v1/flights/recordings", s.recordings)
	mux.HandleFunc("GET /api/v1/flights/history", s.flightHistory)
	mux.HandleFunc("GET /api/v1/flights/history/stream", s.flightHistoryStream)
	mux.HandleFunc("GET /api/v1/flights/history/window", s.flightHistoryWindow)
	mux.HandleFunc("GET /api/v1/flights/history/tracks", s.flightHistoryTracks)
	mux.HandleFunc("GET /api/v1/flights/trace", s.flightTrace)
	mux.HandleFunc("GET /api/v1/flights/photo", s.flightPhoto)
	mux.HandleFunc("GET /api/v1/flights/metadata", s.flightMetadata)
	mux.HandleFunc("POST /api/v1/flights/sight-bell", s.sightBell)
	mux.HandleFunc("GET /api/v1/events", s.emptyEvents)
	mux.HandleFunc("GET /api/v1/dwarf/downloads", s.emptyDownloads)
	mux.HandleFunc("GET /", s.static)
	return s.withRequestLog(mux)
}

func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
		s.logger.Debug("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
}
func (s *Server) currentConfig() config.Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	_, err := os.Stat(s.dataRoot)
	writeJSON(w, http.StatusOK, map[string]any{"version": "0.1.0", "startedAt": s.started, "uptimeSeconds": int(time.Since(s.started).Seconds()), "accessMode": s.currentConfig().Server.AccessMode, "dataRootWritable": err == nil, "services": map[string]string{"flight": "not configured", "dwarf": "not configured", "tracking": "idle", "publishing": "not configured"}})
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.currentConfig())
}
func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if err := s.store.Save(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.configMu.Lock()
	s.config = cfg
	s.configMu.Unlock()
	s.flights.SetReceiver(flight.Receiver{Name: cfg.Flight.HomeName, Latitude: cfg.Flight.Latitude, Longitude: cfg.Flight.Longitude})
	s.flights.SetAircraftExpiry(cfg.Flight.AircraftExpiry())
	writeJSON(w, http.StatusOK, cfg)
}
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.jobs.List()})
}
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.Get(r.PathValue("id"))
	if job.ID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.Cancel(r.PathValue("id")) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "active job not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) startDemoJob(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.Start("diagnostic", func(ctx context.Context, progress func(float64, string)) error {
		for i := 0; i < 5; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
				progress(float64(i+1)/5, "Running diagnostics")
			}
		}
		return nil
	})
	writeJSON(w, http.StatusAccepted, job)
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	f, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		write := func() {
			data, _ := json.Marshal(map[string]any{"type": "status", "timestamp": time.Now().UTC(), "payload": map[string]any{"jobs": s.jobs.List()}})
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			f.Flush()
		}
		write()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) liveFlights(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.flights.LiveState())
}
func (s *Server) flightStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	f, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	write := func() {
		payload, _ := json.Marshal(map[string]any{"type": "flight.snapshot", "timestamp": time.Now().UTC(), "payload": s.flights.LiveState()})
		_, _ = w.Write([]byte("event: flight.snapshot\ndata: " + string(payload) + "\n\n"))
		f.Flush()
	}
	write()
	heartbeat := time.NewTicker(time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			write()
		}
	}
}
func (s *Server) ingestFlightFrame(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Frame string `json:"frame"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid frame request"})
		return
	}
	message, err := s.flights.ConsumeLine(request.Frame)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, message)
}
func (s *Server) recordings(w http.ResponseWriter, r *http.Request) {
	dates, err := flight.ReplayDates(s.currentConfig().Flight.TrackingDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list recordings: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dates": dates})
}
func (s *Server) flightHistory(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date is required"})
		return
	}
	cfg := s.currentConfig()
	replay, err := flight.ReadReplayWithReceiver(cfg.Flight.TrackingDir, date, flight.Receiver{Latitude: cfg.Flight.Latitude, Longitude: cfg.Flight.Longitude}, cfg.Flight.AircraftExpiry())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, replay)
}
func (s *Server) flightHistoryStream(w http.ResponseWriter, r *http.Request) {
	s.writeReplayStream(w, r, false)
}
func (s *Server) flightHistoryWindow(w http.ResponseWriter, r *http.Request) {
	s.writeReplayStream(w, r, true)
}
func (s *Server) writeReplayStream(w http.ResponseWriter, r *http.Request, window bool) {
	date := r.URL.Query().Get("date")
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Format("2006-01-02") != date {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid replay date"})
		return
	}
	var from *int64
	if value := r.URL.Query().Get("from"); value != "" {
		v, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || v < 0 || !window {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid replay cursor"})
			return
		}
		from = &v
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(w)
	started := false
	write := func(value any) error {
		if err := encoder.Encode(value); err != nil {
			return err
		}
		started = true
		flusher.Flush()
		return nil
	}
	cfg := s.currentConfig()
	header := func(header flight.Replay) error { return write(map[string]any{"type": "header", "replay": header}) }
	batch := func(events []flight.ReplayEvent) error {
		return write(map[string]any{"type": "chunk", "events": events})
	}
	receiver := flight.Receiver{Latitude: cfg.Flight.Latitude, Longitude: cfg.Flight.Longitude}
	if window {
		err = flight.StreamReplayWindow(r.Context(), cfg.Flight.TrackingDir, date, receiver, from, header, batch, cfg.Flight.AircraftExpiry())
	} else {
		err = flight.StreamReplay(r.Context(), cfg.Flight.TrackingDir, date, receiver, header, batch, cfg.Flight.AircraftExpiry())
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.logger.Error("stream replay", "error", err, "date", date)
		if !started {
			http.Error(w, "recorded history unavailable", http.StatusInternalServerError)
		} else {
			_ = write(map[string]any{"type": "error", "error": "recorded history could not be read"})
		}
		return
	}
	_ = write(map[string]any{"type": "end"})
}
func (s *Server) emptyEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "nextCursor": nil})
}
func (s *Server) emptyDownloads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	content, err := fs.ReadFile(assets, "static/"+name)
	if err != nil {
		content, err = fs.ReadFile(assets, "static/index.html")
	}
	if err != nil {
		http.Error(w, "UI is unavailable", http.StatusInternalServerError)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(content)
}
