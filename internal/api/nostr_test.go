package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jlambert/trackflights/internal/sdr/nostrpub"
	"github.com/nbd-wtf/go-nostr"
)

var nostrPositionFixture = nostrpub.Position{Latitude: 52.3, Longitude: 4.1, Reference: "odd"}

func TestNostrSignalPositionAndDirectSourceSwitch(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC().Add(-time.Second)
	item := relaySignal{Frame: "8D40621D58C382D690C8AC2863A7", Received: now}
	item.Aircraft.ICAO = "40621D"
	item.Aircraft.Position = &nostrPositionFixture
	item.Aircraft.Position.Timestamp = now
	s.consumeRelaySignal(item)
	state := s.flights.LiveState()
	if len(state.Aircraft) != 1 || state.Aircraft[0].Latitude == nil || *state.Aircraft[0].Latitude != 52.3 {
		t.Fatalf("relay position missing: %+v", state)
	}
	before := state.Aircraft[0].Frames
	body := `[{"frame":"8D40621D58C382D690C8AC2863A7","received":"` + now.Add(time.Second).Format(time.RFC3339Nano) + `"}]`
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/sdr/frames", strings.NewReader(body)))
	if got := s.flights.LiveState().Aircraft[0].Frames; got != before {
		t.Fatalf("direct source also counted: %d", got)
	}
	disabled := false
	s.configMu.Lock()
	s.config.Flight.NostrEnabled = &disabled
	s.configMu.Unlock()
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/sdr/frames", strings.NewReader(body)))
	if got := s.flights.LiveState().Aircraft[0].Frames; got != before+1 {
		t.Fatalf("direct source not resumed: %d", got)
	}
}

func TestReceiveNostrRelay(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	key := nostr.GeneratePrivateKey()
	now := time.Now().UTC().Add(-time.Second)
	content, _ := json.Marshal(map[string]any{"type": "trackflights.sdr.signals", "version": 2, "signals": []any{map[string]any{"frame": "8D40621D58C382D690C8AC2863A7", "received": now, "aircraft": map[string]any{"icao": "40621D", "position": map[string]any{"latitude": 52.3, "longitude": 4.1, "timestamp": now, "reference": "odd"}}}}})
	event := nostr.Event{Kind: 9090, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"t", "trackflights-sdr"}, {"v", "2"}}, Content: string(content)}
	if err := event.Sign(key); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var req []json.RawMessage
		if wsjson.Read(r.Context(), conn, &req) != nil || len(req) < 2 {
			return
		}
		var id string
		json.Unmarshal(req[1], &id)
		wsjson.Write(r.Context(), conn, []any{"EVENT", id, event})
		wsjson.Write(r.Context(), conn, []any{"EOSE", id})
		<-r.Context().Done()
	}))
	srv.Listener = listener
	srv.Start()
	defer srv.Close()
	s := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.receiveNostr(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), event.PubKey, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(s.flights.LiveState().Aircraft) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	state := s.flights.LiveState()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("subscription did not stop")
	}
	if len(state.Aircraft) != 1 || state.Aircraft[0].Latitude == nil || *state.Aircraft[0].Latitude != 52.3 {
		t.Fatalf("relay event not applied: %+v", state)
	}
}

func TestRelayAcceptsOnlyVersionTwo(t *testing.T) {
	key := nostr.GeneratePrivateKey()
	makeEvent := func(version int) *nostr.Event {
		t.Helper()
		content, err := json.Marshal(map[string]any{"type": "trackflights.sdr.signals", "version": version, "signals": []any{map[string]any{"frame": "8D40621D58C382D690C8AC2863A7", "received": time.Now().UTC(), "aircraft": map[string]any{"icao": "40621D"}}}})
		if err != nil {
			t.Fatal(err)
		}
		event := &nostr.Event{Kind: nostrpub.Kind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"t", "trackflights-sdr"}}, Content: string(content)}
		if err = event.Sign(key); err != nil {
			t.Fatal(err)
		}
		return event
	}
	filter := nostr.Filter{Kinds: []int{nostrpub.Kind}, Tags: nostr.TagMap{"t": []string{"trackflights-sdr"}}}
	if got := parseRelayEvent(makeEvent(1), filter, ""); got != nil {
		t.Fatal("version 1 event accepted")
	}
	if got := parseRelayEvent(makeEvent(2), filter, ""); len(got) != 1 {
		t.Fatal("version 2 event rejected")
	}
}
