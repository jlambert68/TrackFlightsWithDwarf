package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/nbd-wtf/go-nostr"
)

func TestCompletedTrackingPublishesSignedSummary(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	received := make(chan nostr.Event, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var msg []json.RawMessage
		if wsjson.Read(r.Context(), conn, &msg) != nil || len(msg) != 2 {
			return
		}
		var event nostr.Event
		if json.Unmarshal(msg[1], &event) != nil {
			return
		}
		received <- event
		_ = wsjson.Write(r.Context(), conn, []any{"OK", event.ID, true, ""})
		<-r.Context().Done()
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	s := newTestServer(t)
	enabled, no := true, false
	zero := 0.0
	s.config.Tracking.NostrEnabled = &enabled
	s.config.Tracking.NostrAutoStart = &no
	s.config.Tracking.NostrRelayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	s.config.Tracking.NostrMinTrackDistance = &zero
	dir := filepath.Join(s.trackingEventsRoot(), "event-one")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(`{"unique_objects":3,"duration_seconds":2.5}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracking.json"), []byte(`{"frames":[{"source_frame":5,"tracks":[{"id":7,"x":10,"y":20,"speed_px_s":4}]},{"source_frame":9,"tracks":[{"id":7,"x":16,"y":24,"speed_px_s":8}]}]}`), 0640); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(t.TempDir(), "worker")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\n", fmt.Sprintf(`{"type":"event","directory":%q}`, dir))
	if err := os.WriteFile(worker, []byte(script), 0750); err != nil {
		t.Fatal(err)
	}
	if err := s.runTracking(context.Background(), worker, "file", "/private/input.mp4", "general", false, false, false, trackingOptions{}, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-received:
		valid, err := event.CheckSignature()
		if err != nil || !valid || event.Kind != 1 {
			t.Fatalf("invalid signed note: %+v %v", event, err)
		}
		var content struct {
			Type       string `json:"type"`
			Version    int    `json:"version"`
			Source     string `json:"source"`
			EventCount int    `json:"eventCount"`
			Events     []struct {
				ID      string `json:"id"`
				Objects int    `json:"objects"`
				Tracks  []struct {
					ID   int `json:"id"`
					Last struct {
						X int `json:"x"`
					} `json:"last"`
				} `json:"tracks"`
			} `json:"events"`
		}
		if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
			t.Fatal(err)
		}
		if content.Type != "trackflights.tracking.analysis" || content.Version != 1 || content.Source != "input.mp4" || content.EventCount != 1 || len(content.Events) != 1 || content.Events[0].ID != "event-one" || content.Events[0].Objects != 3 {
			t.Fatalf("unexpected content: %s", event.Content)
		}
		if strings.Contains(event.Content, "/private/") {
			t.Fatal("published an absolute source path")
		}
		if len(content.Events[0].Tracks) != 1 || content.Events[0].Tracks[0].ID != 7 || content.Events[0].Tracks[0].Last.X != 16 {
			t.Fatalf("missing track summary: %s", event.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no published note")
	}
	info, err := os.Stat(filepath.Join(s.dataRoot, "tracking", "nostr.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private key permissions: %v %v", info, err)
	}
}

func TestTrackingRelayUnavailableReportsPublicationFailure(t *testing.T) {
	s := newTestServer(t)
	no := false
	cfg := s.currentConfig().Tracking
	cfg.NostrAutoStart = &no
	cfg.NostrRelayURL = "ws://127.0.0.1:1"
	if err := os.MkdirAll(s.trackingEventsRoot(), 0750); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.publishTrackingResults(ctx, cfg, "input.mp4", "file", nil, ""); err == nil {
		t.Fatal("publication unexpectedly succeeded without a relay")
	}
}

func TestTrackingPublishRelayRejection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var msg []json.RawMessage
		if wsjson.Read(r.Context(), conn, &msg) != nil || len(msg) != 2 {
			return
		}
		var event nostr.Event
		if json.Unmarshal(msg[1], &event) != nil {
			return
		}
		_ = wsjson.Write(r.Context(), conn, []any{"OK", event.ID, false, "blocked: test rejection"})
		<-r.Context().Done()
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	s := newTestServer(t)
	cfg := s.currentConfig().Tracking
	cfg.NostrRelayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	if err := os.MkdirAll(s.trackingEventsRoot(), 0750); err != nil {
		t.Fatal(err)
	}
	err = s.publishTrackingResults(context.Background(), cfg, "input.mp4", "file", nil, "")
	if err == nil || !strings.Contains(err.Error(), "test rejection") {
		t.Fatalf("rejection not reported: %v", err)
	}
}

func TestTrackingPublicationFiltersAndUploadsQualifyingObjects(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	received := make(chan nostr.Event, 1)
	relay := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var msg []json.RawMessage
		if wsjson.Read(r.Context(), conn, &msg) != nil || len(msg) != 2 {
			return
		}
		var event nostr.Event
		if json.Unmarshal(msg[1], &event) != nil {
			return
		}
		received <- event
		_ = wsjson.Write(r.Context(), conn, []any{"OK", event.ID, true, ""})
		<-r.Context().Done()
	}))
	relay.Listener = listener
	relay.Start()
	defer relay.Close()
	uploads := make(chan string, 3)
	blossom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		hash := sha256.Sum256(data)
		hashText := hex.EncodeToString(hash[:])
		if r.Method != http.MethodPut || r.URL.Path != "/upload" || r.Header.Get("X-SHA-256") != hashText {
			t.Errorf("invalid upload request")
			http.Error(w, "bad upload", 400)
			return
		}
		encoded := strings.TrimPrefix(r.Header.Get("Authorization"), "Nostr ")
		raw, err := base64.StdEncoding.DecodeString(encoded)
		var auth nostr.Event
		if err != nil || json.Unmarshal(raw, &auth) != nil {
			t.Errorf("invalid auth")
			http.Error(w, "bad auth", 400)
			return
		}
		valid, err := auth.CheckSignature()
		if err != nil || !valid || auth.Kind != 24242 {
			t.Errorf("invalid auth signature")
			http.Error(w, "bad auth", 400)
			return
		}
		uploads <- hashText
		_ = json.NewEncoder(w).Encode(map[string]any{"sha256": hashText})
	}))
	defer blossom.Close()
	s := newTestServer(t)
	cfg := s.currentConfig().Tracking
	cfg.NostrRelayURL = "ws" + strings.TrimPrefix(relay.URL, "http")
	cfg.BlossomURL = blossom.URL
	cfg.BlossomPublicURL = "https://blobs.example.test"
	dir := filepath.Join(s.trackingEventsRoot(), "event-one")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(`{"unique_objects":3}`), 0640); err != nil {
		t.Fatal(err)
	}
	data := `{"frames":[{"source_frame":1,"tracks":[{"id":1,"x":0,"y":0},{"id":2,"x":0,"y":0},{"id":3,"x":0,"y":0}]},{"source_frame":2,"tracks":[{"id":1,"x":799,"y":0},{"id":2,"x":800,"y":0},{"id":3,"x":1000,"y":0}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "tracking.json"), []byte(data), 0640); err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 3; id++ {
		cropDir := filepath.Join(dir, "track_crops", fmt.Sprintf("object_%04d", id))
		if err := os.MkdirAll(cropDir, 0750); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 8, 8))
		img.Set(0, 0, color.RGBA{R: uint8(id * 50), A: 255})
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, img, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cropDir, "frame_000001_000000000ms.jpg"), encoded.Bytes(), 0640); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.publishTrackingResults(context.Background(), cfg, "input.mp4", "file", []string{"event-one"}, ""); err != nil {
		t.Fatal(err)
	}
	event := <-received
	var result struct {
		Events []struct {
			Tracks []struct {
				ID     int           `json:"id"`
				Travel float64       `json:"travelDistancePx"`
				Media  *blossomMedia `json:"media"`
			} `json:"tracks"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || len(result.Events[0].Tracks) != 2 {
		t.Fatalf("wrong objects: %s", event.Content)
	}
	tracks := result.Events[0].Tracks
	if tracks[0].ID != 3 || tracks[1].ID != 2 || tracks[1].Travel != 800 {
		t.Fatalf("wrong order or threshold: %+v", tracks)
	}
	if tracks[0].Media == nil || !strings.HasPrefix(tracks[0].Media.URL, "https://blobs.example.test/") || len(event.Tags) != 2 || len(uploads) != 2 {
		t.Fatalf("media missing: %s tags=%v uploads=%d", event.Content, event.Tags, len(uploads))
	}
}

func TestTrackingPublicationFitsRelayContentLimit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	received := make(chan nostr.Event, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var msg []json.RawMessage
		if wsjson.Read(r.Context(), conn, &msg) != nil || len(msg) != 2 {
			return
		}
		var event nostr.Event
		if json.Unmarshal(msg[1], &event) != nil {
			return
		}
		received <- event
		_ = wsjson.Write(r.Context(), conn, []any{"OK", event.ID, true, ""})
		<-r.Context().Done()
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	s := newTestServer(t)
	cfg := s.currentConfig().Tracking
	cfg.NostrRelayURL = "ws" + strings.TrimPrefix(server.URL, "http")
	var ids []string
	for i := range 100 {
		id := fmt.Sprintf("event-%03d", i)
		ids = append(ids, id)
		dir := filepath.Join(s.trackingEventsRoot(), id)
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(map[string]any{"started_at": strings.Repeat("x", 300)})
		if err := os.WriteFile(filepath.Join(dir, "event.json"), data, 0640); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.publishTrackingResults(context.Background(), cfg, "input.mp4", "file", ids, ""); err != nil {
		t.Fatal(err)
	}
	event := <-received
	if len(event.Content) > 15000 {
		t.Fatalf("relay content too long: %d", len(event.Content))
	}
	var content struct {
		EventCount      int               `json:"eventCount"`
		EventsTruncated bool              `json:"eventsTruncated"`
		Events          []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		t.Fatal(err)
	}
	if content.EventCount != 100 || !content.EventsTruncated || len(content.Events) >= 100 {
		t.Fatalf("missing truncation indicator: %+v", content)
	}
}

func TestNostrMonitorReceivesUnfilteredEvents(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	event := nostr.Event{CreatedAt: nostr.Now(), Kind: 42, Content: "unfiltered test", Tags: nostr.Tags{}}
	if err := event.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatal(err)
	}
	filters := make(chan map[string]json.RawMessage, 1)
	relay := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var msg []json.RawMessage
		if wsjson.Read(r.Context(), conn, &msg) != nil || len(msg) < 3 {
			return
		}
		var id string
		var filter map[string]json.RawMessage
		_ = json.Unmarshal(msg[1], &id)
		_ = json.Unmarshal(msg[2], &filter)
		filters <- filter
		_ = wsjson.Write(r.Context(), conn, []any{"EVENT", id, event})
		_ = wsjson.Write(r.Context(), conn, []any{"EOSE", id})
		<-r.Context().Done()
	}))
	relay.Listener = listener
	relay.Start()
	defer relay.Close()
	s := newTestServer(t)
	s.config.Tracking.NostrRelayURL = "ws" + strings.TrimPrefix(relay.URL, "http")
	appListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	app := httptest.NewUnstartedServer(s.Handler())
	app.Listener = appListener
	app.Start()
	defer app.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, app.URL+"/api/v1/nostr/messages/stream?relay=analysis", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	found := false
	for !found {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "unfiltered test") {
			found = true
		}
	}
	filter := <-filters
	if _, ok := filter["kinds"]; ok {
		t.Fatal("monitor unexpectedly filters kinds")
	}
	if _, ok := filter["authors"]; ok {
		t.Fatal("monitor unexpectedly filters authors")
	}
	cancel()
}
