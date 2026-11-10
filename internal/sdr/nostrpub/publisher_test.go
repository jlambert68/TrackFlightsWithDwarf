package nostrpub

import (
	"context"
	"encoding/json"
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
	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestKeyAndVersionedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sdr", "key")
	key, err := LoadKey(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadKey(path)
	if err != nil || key != again {
		t.Fatal("key was not persisted", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key permissions: %v %v", info, err)
	}
	event, err := New("", key).event([]Signal{{Frame: "8D40621D58C382D690C8AC2863A7", Received: time.Now().UTC(), Aircraft: Aircraft{ICAO: "40621D"}}})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := event.CheckSignature()
	if err != nil || !valid {
		t.Fatalf("signature: %v %v", valid, err)
	}
	if event.Kind != Kind || len(event.Tags) != 2 || event.Tags[1][1] != "2" {
		t.Fatalf("unexpected event metadata: %+v", event)
	}
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource("schema.json", strings.NewReader(string(schemaV2))); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var content any
	if err = json.Unmarshal([]byte(event.Content), &content); err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(content); err != nil {
		t.Fatal(err)
	}
	invalid := strings.Replace(event.Content, `"version":2`, `"version":1`, 1)
	if err = json.Unmarshal([]byte(invalid), &content); err != nil {
		t.Fatal(err)
	}
	if schema.Validate(content) == nil {
		t.Fatal("schema accepted an unsupported version")
	}
	if err = os.WriteFile(path, []byte("00"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadKey(path); err == nil {
		t.Fatal("accepted invalid key")
	}
}

func TestPublishBatchToRelay(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	got := make(chan nostr.Event, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		var msg []json.RawMessage
		if err = wsjson.Read(r.Context(), conn, &msg); err != nil || len(msg) != 2 {
			return
		}
		var event nostr.Event
		if json.Unmarshal(msg[1], &event) != nil {
			return
		}
		got <- event
		_ = wsjson.Write(r.Context(), conn, []any{"OK", event.ID, true, ""})
	}))
	srv.Listener = listener
	srv.Start()
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := New("ws"+strings.TrimPrefix(srv.URL, "http"), nostr.GeneratePrivateKey())
	done := make(chan struct{})
	go func() { publisher.Run(ctx); close(done) }()
	publisher.Add("8D40621D58C382D690C8AC2863A7", time.Now().UTC())
	select {
	case event := <-got:
		ok, err := event.CheckSignature()
		if err != nil || !ok {
			t.Fatalf("invalid event signature: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not receive event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher failed to stop")
	}
}

func TestDecodePositionAndSchema(t *testing.T) {
	p := New("", nostr.GeneratePrivateKey())
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	p.Add("8D40621D202CC371C32CE02E065C", now.Add(-2*time.Second))
	p.Add("8D40621D994409940838174550B1", now.Add(-time.Second))
	p.Add("8D40621D58C382D690C8AC2863A7", now)
	p.Add("8D40621D58C386435CC412692AD6", now.Add(time.Second))
	callsign, velocity, first, second := <-p.queue, <-p.queue, <-p.queue, <-p.queue
	if first.Aircraft.ICAO != "40621D" || first.Aircraft.Position != nil {
		t.Fatalf("first: %+v", first)
	}
	if second.Aircraft.Position == nil || second.Aircraft.Position.Latitude < 52 || second.Aircraft.Position.Latitude > 53 || !second.Aircraft.Position.Timestamp.Equal(now.Add(time.Second)) {
		t.Fatalf("decoded: %+v", second)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("v2.json", strings.NewReader(string(schemaV2))); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("v2.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := p.event([]Signal{callsign, velocity, first, second})
	if err != nil {
		t.Fatal(err)
	}
	var actual any
	if err = json.Unmarshal([]byte(event.Content), &actual); err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(actual); err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("example-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected any
	if err = json.Unmarshal(example, &expected); err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(expected); err != nil {
		t.Fatal(err)
	}
	if string(example) != event.Content { // Compare parsed JSON values rather than formatting.
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(expected)
		if string(a) != string(b) {
			t.Fatalf("example does not match decoder: %s", a)
		}
	}
}
