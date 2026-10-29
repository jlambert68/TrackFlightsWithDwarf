package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jlambert/trackflights/internal/config"
	"github.com/jlambert/trackflights/internal/flight"
	"github.com/jlambert/trackflights/internal/jobs"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	store := config.NewStore(filepath.Join(root, "config.json"))
	cfg := config.Default()
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, store, jobs.NewManager(), flight.NewService(), root)
}

func TestAircraftExpirySettingPersistsAndUpdatesReplay(t *testing.T) {
	server := newTestServer(t)
	cfg := server.currentConfig()
	cfg.Flight.TrackingDir = filepath.Join(t.TempDir(), "no-recordings")
	seconds := 45
	cfg.Flight.AircraftExpirySeconds = &seconds
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	loaded, err := server.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Flight.AircraftExpiry() != 45*time.Second {
		t.Fatalf("persisted expiry: %v", loaded.Flight.AircraftExpiry())
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/history/window?date=2026-10-13", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"aircraftExpirySeconds":45`) {
		t.Fatalf("replay: %d %s", response.Code, response.Body.String())
	}
}

func TestFlightTraceValidatesRequest(t *testing.T) {
	server := newTestServer(t)
	for _, query := range []string{"?icao=../../etc&before=2026-10-13T12:00:00Z", "?icao=4AAB15&before=bad"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/trace"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query %s: got %d", query, response.Code)
		}
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFlightTraceUsesConfiguredLookback(t *testing.T) {
	server := newTestServer(t)
	minutes := 10
	server.config.Flight.HistoryLookbackMinutes = &minutes
	before := time.Date(2020, 10, 8, 19, 10, 0, 0, time.UTC)
	server.history.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/globe_history/2020/10/08/traces/23/trace_full_abc123.json" {
			t.Fatalf("unexpected archive path: %s", r.URL.Path)
		}
		body := fmt.Sprintf(`{"icao":"abc123","timestamp":%d,"trace":[[0,59,18,10000],[300,59.1,18.1,11000]]}`, before.Add(-9*time.Minute).Unix())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/trace?icao=ABC123&before="+before.Format(time.RFC3339), nil))
	var result struct {
		LookbackSeconds int `json:"lookbackSeconds"`
		Points          []struct {
			Latitude float64 `json:"latitude"`
		} `json:"points"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.LookbackSeconds != 600 || len(result.Points) != 2 {
		t.Fatalf("unexpected trace: %d %+v", response.Code, result)
	}
	minutes = 0
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/trace?icao=ABC123&before="+before.Format(time.RFC3339), nil))
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.LookbackSeconds != 0 || len(result.Points) != 0 {
		t.Fatalf("disabled lookback: %+v", result)
	}
}

func TestFlightTraceReportsUpstream403(t *testing.T) {
	server := newTestServer(t)
	server.history.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("blocked"))}, nil
	})}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/trace?icao=ABC123&before=2020-10-08T19:10:00Z", nil))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "HTTP 403") {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestFlightPhotoRejectsUnsafeIdentifiers(t *testing.T) {
	server := newTestServer(t)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/photo?icao=4AAB15&registration=BAD%2FVALUE", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid") {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestSightBellOptInAndRateLimit(t *testing.T) {
	server := newTestServer(t)
	calls := 0
	server.playBell = func(context.Context) error { calls++; return nil }
	request := func(origin string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/flights/sight-bell", strings.NewReader(`{"test":true}`))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response.Code
	}
	if got := request(""); got != http.StatusServiceUnavailable || calls != 0 {
		t.Fatalf("disabled: %d calls=%d", got, calls)
	}
	server.config.Flight.HostBellEnabled = true
	if got := request("https://evil.example"); got != http.StatusForbidden || calls != 0 {
		t.Fatalf("cross-origin: %d calls=%d", got, calls)
	}
	if got := request(""); got != http.StatusNoContent {
		t.Fatalf("bell: %d", got)
	}
	if got := request(""); got != http.StatusNoContent || calls != 1 {
		t.Fatalf("rate limit: %d calls=%d", got, calls)
	}
}

func TestReplayStreamEmitsHeaderChunksAndEnd(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	dir := filepath.Join(root, "4AAB15")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-10-13.jsonl"), []byte("{\"event\":\"adsb_frame\",\"icao\":\"4AAB15\",\"receivedAt\":\"2026-10-13T17:00:00Z\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	server.config.Flight.TrackingDir = root
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/history/stream?date=2026-10-13", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	decoder := json.NewDecoder(response.Body)
	for _, want := range []string{"header", "chunk", "end"} {
		var message struct {
			Type string `json:"type"`
		}
		if err := decoder.Decode(&message); err != nil || message.Type != want {
			t.Fatalf("expected %s: %+v, %v", want, message, err)
		}
	}
}

func TestReplayWindowRejectsInvalidCursor(t *testing.T) {
	server := newTestServer(t)
	for _, cursor := range []string{"-1", "invalid", "9223372036854775808"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/history/window?date=2026-10-13&from="+cursor, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("cursor %s: status %d", cursor, response.Code)
		}
	}
}

func TestHealthAndStatus(t *testing.T) {
	server := newTestServer(t)
	for _, endpoint := range []string{"/api/v1/health", "/api/v1/status"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, endpoint, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", endpoint, response.Code)
		}
	}
}

func TestFlightFrameIngestUpdatesSnapshot(t *testing.T) {
	server := newTestServer(t)
	body := bytes.NewBufferString(`{"frame":"*8D4840D6202CC371C32CE0576098;"}`)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/flights/frames", body))
	if response.Code != http.StatusAccepted {
		t.Fatalf("ingest = %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/live", nil))
	var payload struct {
		Aircraft []struct {
			ICAO string `json:"icao"`
		} `json:"aircraft"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Aircraft) != 1 || payload.Aircraft[0].ICAO != "4840D6" {
		t.Fatalf("unexpected snapshot: %+v", payload.Aircraft)
	}
}

func TestSettingsUpdateChangesLiveReceiver(t *testing.T) {
	server := newTestServer(t)
	cfg := config.Default()
	cfg.Flight.HomeName = "Test home"
	cfg.Flight.Latitude = 57.5
	cfg.Flight.Longitude = 12.1
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(data)))
	if response.Code != http.StatusOK {
		t.Fatalf("update: %d %s", response.Code, response.Body.String())
	}
	receiver := server.flights.LiveState().Receiver
	if receiver.Name != cfg.Flight.HomeName || receiver.Latitude != cfg.Flight.Latitude || receiver.Longitude != cfg.Flight.Longitude {
		t.Fatalf("receiver was not updated: %+v", receiver)
	}
}
