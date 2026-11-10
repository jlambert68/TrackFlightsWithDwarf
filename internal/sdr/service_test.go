package sdr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHistoryAndDelivery(t *testing.T) {
	s := New()
	s.Add("8D40621D58C382D690C8AC2863A7")
	s.Add("invalid")
	if got := s.History(0); len(got) != 1 {
		t.Fatalf("history: %v", got)
	}
	received := make(chan []Frame, 1)
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("X-SDR-Signal"); got != "receiver_unavailable" {
			t.Errorf("signal header = %q", got)
		}
		var f []Frame
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			t.Error(err)
		}
		select {
		case received <- f:
		default:
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.deliver(ctx, "http://main.test/frames")
	select {
	case frames := <-received:
		if len(frames) != 1 || frames[0].ID != 1 {
			t.Fatalf("delivery: %v", frames)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delivery timeout")
	}
	s.mu.Lock()
	s.frames[0].Received = time.Now().Add(-16 * time.Minute)
	s.mu.Unlock()
	if len(s.History(0)) != 0 {
		t.Fatal("expired frame retained")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSignalStatus(t *testing.T) {
	s := New()
	if got := s.SignalStatus(); got != "receiver_unavailable" {
		t.Fatal(got)
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	if got := s.SignalStatus(); got != "no_signal" {
		t.Fatal(got)
	}
	s.Add("invalid")
	if got := s.SignalStatus(); got != "no_signal" {
		t.Fatal(got)
	}
	s.Add("8D40621D58C382D690C8AC2863A7")
	if got := s.SignalStatus(); got != "receiving" {
		t.Fatal(got)
	}
	s.mu.Lock()
	s.lastSignal = time.Now().Add(-31 * time.Second)
	s.mu.Unlock()
	if got := s.SignalStatus(); got != "no_signal" {
		t.Fatal(got)
	}
}

func TestVersionedSchemaEndpoints(t *testing.T) {
	s := New()
	for _, test := range []struct {
		path     string
		status   int
		contains string
	}{
		{"/api/v1/schemas/flight-signals", 200, `"latestVersion":2`},
		{"/api/v1/schemas/flight-signals/1", 404, ""},
		{"/api/v1/schemas/flight-signals/2", 200, `"const": 2`},
		{"/api/v1/schemas/flight-signals/3", 404, ""},
	} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest("GET", test.path, nil))
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("%s: %d %s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestSignalHandlerReceivesOnlyValidNormalizedFrames(t *testing.T) {
	s := New()
	var frame string
	var received time.Time
	s.SetSignalHandler(func(f string, at time.Time) { frame = f; received = at })
	s.Add("bad")
	if frame != "" {
		t.Fatal("invalid frame was delivered")
	}
	s.Add("*8d40621d58c382d690c8ac2863a7;")
	if frame != "8D40621D58C382D690C8AC2863A7" || received.IsZero() {
		t.Fatalf("frame %q at %s", frame, received)
	}
}
