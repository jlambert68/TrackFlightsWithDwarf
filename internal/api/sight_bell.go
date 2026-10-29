package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

func (s *Server) sightBell(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") || !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if !s.currentConfig().Flight.HostBellEnabled {
		http.Error(w, "host bell disabled", http.StatusServiceUnavailable)
		return
	}
	var request struct {
		Test     bool     `json:"test"`
		Aircraft []string `json:"aircraft"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request); err != nil || (request.Test && len(request.Aircraft) != 0) || (!request.Test && (len(request.Aircraft) == 0 || len(request.Aircraft) > 256)) {
		http.Error(w, "invalid bell request", http.StatusBadRequest)
		return
	}
	for _, icao := range request.Aircraft {
		if len(icao) != 6 {
			http.Error(w, "invalid ICAO", http.StatusBadRequest)
			return
		}
		if _, err := hex.DecodeString(icao); err != nil {
			http.Error(w, "invalid ICAO", http.StatusBadRequest)
			return
		}
	}
	s.bellMu.Lock()
	defer s.bellMu.Unlock()
	if time.Since(s.lastBell) < 4*time.Second {
		if s.lastBellFailed {
			http.Error(w, "host sound unavailable", http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	s.lastBell = time.Now()
	if err := s.playBell(r.Context()); err != nil {
		s.lastBellFailed = true
		http.Error(w, "host sound unavailable", http.StatusServiceUnavailable)
		return
	}
	s.lastBellFailed = false
	w.WriteHeader(http.StatusNoContent)
}

func playHostBell(parent context.Context) error {
	const sound = "/usr/share/sounds/freedesktop/stereo/bell.oga"
	players := []struct {
		name string
		args []string
	}{{"pw-play", []string{sound}}, {"paplay", []string{sound}}, {"canberra-gtk-play", []string{"--file", sound}}, {"gst-play-1.0", []string{"--quiet", "--no-interactive", sound}}, {"ffplay", []string{"-nodisp", "-autoexit", "-loglevel", "quiet", sound}}}
	for _, player := range players {
		path, err := exec.LookPath(player.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, time.Second)
		command := exec.CommandContext(ctx, path, player.args...)
		command.Stdout, command.Stderr = io.Discard, io.Discard
		err = command.Run()
		cancel()
		if err == nil {
			return nil
		}
		if parent.Err() != nil {
			return parent.Err()
		}
	}
	return exec.ErrNotFound
}
