// Package sdr receives rtl_adsb frames and forwards a bounded time window.
package sdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jlambert/trackflights/internal/flight/adsb"
)

type Frame struct {
	ID       uint64    `json:"id"`
	Frame    string    `json:"frame"`
	Received time.Time `json:"received"`
}
type Service struct {
	mu     sync.Mutex
	frames []Frame
	next   uint64
	cancel context.CancelFunc
	client *http.Client
}

func New() *Service { return &Service{client: &http.Client{Timeout: 5 * time.Second}} }
func (s *Service) prune(now time.Time) {
	n := 0
	for n < len(s.frames) && now.Sub(s.frames[n].Received) > 15*time.Minute {
		n++
	}
	if n > 0 {
		s.frames = append([]Frame(nil), s.frames[n:]...)
	}
}
func (s *Service) Add(line string) {
	if _, err := adsb.DecodeLine(line); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.prune(now)
	s.next++
	s.frames = append(s.frames, Frame{s.next, line, now})
}
func (s *Service) History(after uint64) []Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now())
	out := make([]Frame, 0)
	for _, f := range s.frames {
		if f.ID > after {
			out = append(out, f)
		}
	}
	return out
}
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.History(0))
	})
	mux.HandleFunc("POST /api/v1/subscription", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		u, err := url.Parse(req.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			http.Error(w, "invalid callback URL", 400)
			return
		}
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		s.mu.Unlock()
		go s.deliver(ctx, req.URL)
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}

// A subscription replays the retained window and then follows new frames in order.
// Any unsuccessful request ends it; the main application can subscribe again.
func (s *Service) deliver(ctx context.Context, target string) {
	defer s.client.CloseIdleConnections()
	var cursor uint64
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		frames := s.History(cursor)
		if len(frames) > 500 {
			frames = frames[:500]
		}
		body, _ := json.Marshal(frames)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return
		}
		if len(frames) > 0 {
			cursor = frames[len(frames)-1].ID
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) Read(ctx context.Context, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.Add(strings.TrimSpace(scanner.Text()))
	}
	return scanner.Err()
}
func (s *Service) RunReceiver(ctx context.Context, device, gain, ppm string) error {
	args := []string{"-d", device, "-p", ppm}
	if gain != "" && gain != "auto" {
		args = append(args, "-g", gain)
	}
	cmd := exec.CommandContext(ctx, "rtl_adsb", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return err
	}
	readErr := s.Read(ctx, stdout)
	waitErr := cmd.Wait()
	return errors.Join(readErr, waitErr)
}
