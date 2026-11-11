package api

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jlambert/trackflights/internal/config"
	"github.com/jlambert/trackflights/internal/sdr/nostrpub"
	"github.com/nbd-wtf/go-nostr"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// The original tracker used this NIP-01 kind-1 schema. Text-only notes with
// no media tags satisfy it without claiming that files were uploaded.
//
//go:embed tracking-nostr-schema.json
var trackingNoteSchema []byte

var errTrackingMediaUnavailable = errors.New("some object media could not be uploaded to Blossom")

func trackingNostrSchema(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/schema+json")
	_, _ = w.Write(trackingNoteSchema)
}

func (s *Server) connectTrackingRelay(ctx context.Context, cfg config.TrackingConfig) (*nostr.Relay, error) {
	connect := func() (*nostr.Relay, error) {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return nostr.RelayConnect(c, cfg.NostrRelayURL)
	}
	if relay, err := connect(); err == nil {
		return relay, nil
	}
	u, err := url.Parse(cfg.NostrRelayURL)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if !local || cfg.NostrAutoStart == nil || !*cfg.NostrAutoStart || u.Scheme != "ws" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("relay %s is unavailable", cfg.NostrRelayURL)
	}
	s.tracking.relayMu.Lock()
	defer s.tracking.relayMu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if relay, err := connect(); err == nil {
		return relay, nil
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	bind := u.Hostname()
	if bind == "localhost" {
		bind = "127.0.0.1"
	}
	if strings.Contains(bind, ":") {
		bind = "[" + bind + "]"
	}
	startCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(startCtx, "make", "nostr-start-analysis", "DATA_ROOT="+s.dataRoot, "NOSTR_ANALYSIS_BIND="+bind, "NOSTR_ANALYSIS_PORT="+port)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("start analysis relay: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if relay, err := connect(); err == nil {
			return relay, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("analysis relay started but is not responding at %s", cfg.NostrRelayURL)
}

func (s *Server) publishTrackingResults(ctx context.Context, cfg config.TrackingConfig, input, mode string, ids []string, sessionID string) error {
	type point struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	type trackedObject struct {
		ID             int           `json:"id"`
		First          point         `json:"first"`
		Last           point         `json:"last"`
		FirstFrame     int           `json:"firstFrame"`
		LastFrame      int           `json:"lastFrame"`
		MaxSpeed       float64       `json:"maxSpeedPxSec"`
		TravelDistance float64       `json:"travelDistancePx"`
		Media          *blossomMedia `json:"media,omitempty"`
	}
	type eventSummary struct {
		ID           string          `json:"id"`
		StartedAt    string          `json:"startedAt,omitempty"`
		Duration     float64         `json:"durationSeconds,omitempty"`
		Objects      int             `json:"objects,omitempty"`
		HighestSpeed float64         `json:"highestSpeedPxSec,omitempty"`
		Tracks       []trackedObject `json:"tracks"`
	}
	events := make([]eventSummary, 0, min(len(ids), 100))
	var session *eventSummary
	root, err := os.OpenRoot(s.trackingEventsRoot())
	if err != nil {
		return err
	}
	defer root.Close()
	remainingTracks := 100
	tracksLimited := false
	minDistance := 800.0
	if cfg.NostrMinTrackDistance != nil {
		minDistance = *cfg.NostrMinTrackDistance
	}
	allIDs := append([]string(nil), ids...)
	if sessionID != "" {
		allIDs = append(allIDs, sessionID)
	}
	for _, id := range allIDs {
		if len(events) == 100 && id != sessionID {
			continue
		}
		f, err := root.Open(filepath.Join(id, "event.json"))
		if err != nil {
			return fmt.Errorf("read event %s: %w", id, err)
		}
		var summary struct {
			StartedAt    string  `json:"started_at"`
			Duration     float64 `json:"duration_seconds"`
			Objects      int     `json:"unique_objects"`
			HighestSpeed float64 `json:"highest_speed_px_s"`
		}
		err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&summary)
		f.Close()
		if err != nil {
			return fmt.Errorf("decode event %s: %w", id, err)
		}
		item := eventSummary{ID: id, StartedAt: summary.StartedAt, Duration: summary.Duration, Objects: summary.Objects, HighestSpeed: summary.HighestSpeed, Tracks: []trackedObject{}}
		if remainingTracks > 0 {
			if metadata, openErr := root.Open(filepath.Join(id, "tracking.json")); openErr == nil {
				decoder := json.NewDecoder(metadata)
				if token, err := decoder.Token(); err == nil && token == json.Delim('{') {
					for decoder.More() {
						field, err := decoder.Token()
						if err != nil {
							break
						}
						if field != "frames" {
							var value json.RawMessage
							if decoder.Decode(&value) != nil {
								break
							}
							continue
						}
						if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
							break
						}
						indices := map[int]int{}
						for decoder.More() {
							var frame struct {
								SourceFrame int `json:"source_frame"`
								Tracks      []struct {
									ID     int     `json:"id"`
									X      int     `json:"x"`
									Y      int     `json:"y"`
									Speed  float64 `json:"speed_px_s"`
									Missed int     `json:"missed_frames"`
								} `json:"tracks"`
							}
							if decoder.Decode(&frame) != nil {
								break
							}
							for _, track := range frame.Tracks {
								if track.ID <= 0 || track.Missed > 0 {
									continue
								}
								index, ok := indices[track.ID]
								if !ok {
									if len(item.Tracks) == 100000 {
										tracksLimited = true
										continue
									}
									index = len(item.Tracks)
									indices[track.ID] = index
									item.Tracks = append(item.Tracks, trackedObject{ID: track.ID, First: point{track.X, track.Y}, FirstFrame: frame.SourceFrame})
								}
								object := &item.Tracks[index]
								object.Last = point{track.X, track.Y}
								object.LastFrame = frame.SourceFrame
								if track.Speed > object.MaxSpeed {
									object.MaxSpeed = track.Speed
								}
							}
						}
						break
					}
				}
				metadata.Close()
			}
			qualified := item.Tracks[:0]
			for _, object := range item.Tracks {
				object.TravelDistance = math.Hypot(float64(object.Last.X)-float64(object.First.X), float64(object.Last.Y)-float64(object.First.Y))
				if object.TravelDistance >= minDistance {
					qualified = append(qualified, object)
				}
			}
			sort.Slice(qualified, func(i, j int) bool {
				if qualified[i].TravelDistance == qualified[j].TravelDistance {
					return qualified[i].ID < qualified[j].ID
				}
				return qualified[i].TravelDistance > qualified[j].TravelDistance
			})
			if len(qualified) > remainingTracks {
				qualified = qualified[:remainingTracks]
				tracksLimited = true
			}
			remainingTracks -= len(qualified)
			item.Tracks = qualified
		} else {
			tracksLimited = true
		}
		if id == sessionID {
			session = &item
		} else {
			events = append(events, item)
		}
	}
	source := filepath.Base(input)
	if mode == "live" {
		source = "live stream"
	}
	payload := map[string]any{"type": "trackflights.tracking.analysis", "version": 1, "source": source, "mode": mode, "minTrackDistancePx": minDistance, "eventCount": len(ids), "events": events, "eventsTruncated": len(ids) > 100, "tracksLimited": tracksLimited, "session": session, "completedAt": time.Now().UTC()}
	var content []byte
	for {
		content, err = json.Marshal(payload)
		if err != nil {
			return err
		}
		// The bundled relay defaults to 16 KiB of content; leave headroom.
		if len(content) <= 14000 {
			break
		}
		if session != nil && len(session.Tracks) > 0 {
			session.Tracks = session.Tracks[:len(session.Tracks)-1]
			payload["tracksLimited"] = true
			continue
		}
		removedTrack := false
		for i := len(events) - 1; i >= 0; i-- {
			if len(events[i].Tracks) > 0 {
				events[i].Tracks = events[i].Tracks[:len(events[i].Tracks)-1]
				payload["tracksLimited"] = true
				removedTrack = true
				break
			}
		}
		if removedTrack {
			continue
		}
		if len(events) > 0 {
			events = events[:len(events)-1]
			payload["events"] = events
			payload["eventsTruncated"] = true
			continue
		}
		if session != nil {
			session = nil
			payload["session"] = nil
			payload["sessionOmitted"] = true
			continue
		}
		return fmt.Errorf("tracking summary exceeds the relay content limit")
	}
	keyFile := cfg.NostrKeyFile
	if !filepath.IsAbs(keyFile) {
		keyFile = filepath.Join(s.dataRoot, keyFile)
	}
	key, err := nostrpub.LoadKey(keyFile)
	if err != nil {
		return fmt.Errorf("load Nostr key: %w", err)
	}
	tags := nostr.Tags{}
	uploaded := map[string]struct {
		media *blossomMedia
		tag   nostr.Tag
	}{}
	mediaCtx, cancelMedia := context.WithTimeout(ctx, 45*time.Second)
	defer cancelMedia()
	mediaFailed := false
	attach := func(item *eventSummary) {
		if item == nil || mediaFailed {
			return
		}
		for i := range item.Tracks {
			if mediaCtx.Err() != nil {
				mediaFailed = true
				break
			}
			object := &item.Tracks[i]
			data, mimeType, err := objectMedia(root, item.ID, object.ID, cfg.NostrUseObjectGIF)
			if err != nil {
				s.logger.Warn("cannot prepare tracked object media", "event", item.ID, "track", object.ID, "error", err)
				payload["mediaUnavailable"] = true
				continue
			}
			if len(data) == 0 {
				continue
			}
			hash := sha256.Sum256(data)
			cacheKey := hex.EncodeToString(hash[:])
			cached, ok := uploaded[cacheKey]
			if !ok {
				media, tag, err := uploadBlossomMedia(mediaCtx, cfg.BlossomURL, cfg.BlossomPublicURL, key, data, mimeType)
				if err != nil {
					s.logger.Warn("cannot upload tracked object media", "event", item.ID, "track", object.ID, "error", err)
					payload["mediaUnavailable"] = true
					mediaFailed = true
					break
				}
				cached.media, cached.tag = media, tag
			}
			object.Media = cached.media
			candidate, err := json.Marshal(payload)
			if err != nil || len(candidate) > 14900 {
				object.Media = nil
				payload["mediaLimited"] = true
				continue
			}
			if !ok {
				uploaded[cacheKey] = cached
				tags = append(tags, cached.tag)
			}
		}
	}
	for i := range events {
		attach(&events[i])
	}
	attach(session)
	if mediaCtx.Err() != nil {
		payload["mediaUnavailable"] = true
	}
	content, err = json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(content) > 15000 {
		return fmt.Errorf("tracking summary exceeds the relay content limit")
	}
	event := nostr.Event{CreatedAt: nostr.Now(), Kind: nostr.KindTextNote, Tags: tags, Content: string(content)}
	if err := event.Sign(key); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("tracking-schema.json", strings.NewReader(string(trackingNoteSchema))); err != nil {
		return err
	}
	schema, err := compiler.Compile("tracking-schema.json")
	if err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("tracking note does not match schema: %w", err)
	}
	relay, err := s.connectTrackingRelay(ctx, cfg)
	if err != nil {
		return err
	}
	defer relay.Close()
	publishCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := relay.Publish(publishCtx, event); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	s.logger.Info("tracking analysis published", "event", event.ID, "relay", cfg.NostrRelayURL)
	if payload["mediaUnavailable"] == true {
		return errTrackingMediaUnavailable
	}
	return nil
}
