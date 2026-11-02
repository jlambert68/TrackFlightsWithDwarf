package sdr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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
