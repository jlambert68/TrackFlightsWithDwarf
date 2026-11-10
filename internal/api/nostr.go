package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jlambert/trackflights/internal/flight"
	"github.com/jlambert/trackflights/internal/flight/adsb"
	"github.com/jlambert/trackflights/internal/sdr/nostrpub"
	"github.com/nbd-wtf/go-nostr"
)

type relaySignal struct {
	Frame    string    `json:"frame"`
	Received time.Time `json:"received"`
	Aircraft struct {
		ICAO     string             `json:"icao"`
		Position *nostrpub.Position `json:"position"`
	} `json:"aircraft"`
}
type relayMessage struct {
	Type    string        `json:"type"`
	Version int           `json:"version"`
	Signals []relaySignal `json:"signals"`
}

func (s *Server) nostrEnabled() bool {
	cfg := s.currentConfig().Flight
	return cfg.NostrEnabled == nil || *cfg.NostrEnabled
}
func (s *Server) setNostrStatus(status string) {
	s.sdrMu.Lock()
	s.nostrStatus = status
	s.sdrMu.Unlock()
}

// RunNostr reconnects on failure and applies saved configuration changes.
func (s *Server) RunNostr(ctx context.Context, logger *slog.Logger) {
	var cancel context.CancelFunc
	var done <-chan struct{}
	var previous string
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	for {
		cfg := s.currentConfig().Flight
		enabled := cfg.NostrEnabled == nil || *cfg.NostrEnabled
		key := ""
		if enabled {
			key = cfg.NostrRelayURL + "\x00" + cfg.NostrPublisher
		}
		if key != previous {
			if cancel != nil {
				cancel()
				if done != nil {
					select {
					case <-done:
					case <-ctx.Done():
						return
					}
				}
				cancel = nil
			}
			previous = key
			done = nil
			if key == "" {
				s.setNostrStatus("disabled")
			}
		}
		if key != "" && done == nil {
			var child context.Context
			child, cancel = context.WithCancel(ctx)
			finished := make(chan struct{})
			done = finished
			go func(url, pub string) { defer close(finished); s.receiveNostr(child, url, pub, logger) }(cfg.NostrRelayURL, cfg.NostrPublisher)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-done:
			if cancel != nil {
				cancel()
				cancel = nil
			}
			done = nil
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}
}
func (s *Server) receiveNostr(ctx context.Context, url, pub string, logger *slog.Logger) {
	s.setNostrStatus("connecting")
	defer s.setNostrStatus("disconnected")
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	relay, err := nostr.RelayConnect(c, url)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			logger.Warn("Nostr relay connection failed", "error", err)
		}
		return
	}
	defer relay.Close()
	since := nostr.Timestamp(time.Now().Add(-15 * time.Minute).Unix())
	filter := nostr.Filter{Kinds: []int{nostrpub.Kind}, Tags: nostr.TagMap{"t": []string{"trackflights-sdr"}}, Since: &since, Limit: 500}
	if pub != "" {
		filter.Authors = []string{pub}
	}
	sub, err := relay.Subscribe(ctx, nostr.Filters{filter})
	if err != nil {
		logger.Warn("Nostr subscription failed", "error", err)
		return
	}
	defer sub.Unsub()
	s.setNostrStatus("connected")
	var pending []relaySignal
	collecting := true
	eose := sub.EndOfStoredEvents
	flush := func() {
		sort.SliceStable(pending, func(i, j int) bool { return pending[i].Received.Before(pending[j].Received) })
		for _, signal := range pending {
			s.consumeRelaySignal(signal)
		}
		pending = nil
		collecting = false
	}
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-relay.Context().Done():
			return
		case <-timeout.C:
			if collecting {
				flush()
			}
		case <-eose:
			if collecting {
				flush()
			}
			eose = nil
		case event, ok := <-sub.Events:
			if !ok {
				return
			}
			signals := parseRelayEvent(event, filter, pub)
			if signals == nil {
				continue
			}
			if collecting {
				pending = append(pending, signals...)
				if len(pending) >= 12500 {
					flush()
				}
			} else {
				for _, signal := range signals {
					s.consumeRelaySignal(signal)
				}
			}
		}
	}
}

// parseRelayEvent only accepts signed version 2 flight events.
func parseRelayEvent(event *nostr.Event, filter nostr.Filter, pub string) []relaySignal {
	if event == nil || len(event.Content) > 128*1024 || event.Kind != nostrpub.Kind || (pub != "" && event.PubKey != pub) {
		return nil
	}
	valid, err := event.CheckSignature()
	if err != nil || !valid || event.ID != event.GetID() || !filter.Matches(event) {
		return nil
	}
	var msg relayMessage
	if json.Unmarshal([]byte(event.Content), &msg) != nil || msg.Type != nostrpub.EventType || msg.Version != nostrpub.Version || len(msg.Signals) == 0 || len(msg.Signals) > 25 {
		return nil
	}
	return msg.Signals
}
func (s *Server) consumeRelaySignal(item relaySignal) {
	now := time.Now()
	if item.Received.IsZero() || item.Received.Before(now.Add(-15*time.Minute)) || item.Received.After(now.Add(10*time.Second)) {
		return
	}
	decoded, err := adsb.DecodeLine(item.Frame)
	if err != nil {
		return
	}
	var pos *flight.RemotePosition
	if p := item.Aircraft.Position; p != nil && strings.EqualFold(item.Aircraft.ICAO, decoded.ICAO) && !p.Timestamp.Before(item.Received.Add(-15*time.Minute)) {
		pos = &flight.RemotePosition{Latitude: p.Latitude, Longitude: p.Longitude, Timestamp: p.Timestamp, Reference: p.Reference}
	}
	if !s.nostrEnabled() {
		return
	}
	_, _ = s.flights.ConsumeLineWithPosition(item.Frame, item.Received, pos)
}
