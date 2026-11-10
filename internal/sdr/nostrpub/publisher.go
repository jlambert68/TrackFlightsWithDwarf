// Package nostrpub publishes batches of received ADS-B signals as Nostr events.
package nostrpub

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/jlambert/trackflights/internal/flight"
	"github.com/nbd-wtf/go-nostr"
)

const Kind = 9090
const Version = 2
const EventType = "trackflights.sdr.signals"

//go:embed schema-v2.json
var schemaV2 []byte

type Signal struct {
	Frame    string    `json:"frame"`
	Received time.Time `json:"received"`
	Aircraft Aircraft  `json:"aircraft"`
}
type Aircraft struct {
	ICAO         string    `json:"icao"`
	Callsign     string    `json:"callsign,omitempty"`
	AltitudeFeet *int      `json:"altitudeFeet,omitempty"`
	SpeedKnots   *float64  `json:"speedKnots,omitempty"`
	HeadingDeg   *float64  `json:"headingDeg,omitempty"`
	VerticalFPM  *int      `json:"verticalFPM,omitempty"`
	Position     *Position `json:"position,omitempty"`
}
type Position struct {
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Timestamp time.Time `json:"timestamp"`
	Reference string    `json:"reference"`
}
type Message struct {
	Type    string   `json:"type"`
	Version int      `json:"version"`
	Signals []Signal `json:"signals"`
}

func SchemaHandler(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("version") != strconv.Itoa(Version) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/schema+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(schemaV2)
}
func SchemasHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"type": EventType, "latestVersion": Version, "versions": []int{Version}, "schemaPath": "/api/v1/schemas/flight-signals/" + strconv.Itoa(Version)})
}

// LoadKey preserves the receiver's identity across restarts without exposing the key in arguments.
func LoadKey(path string) (string, error) {
	read := func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		key := strings.TrimSpace(string(b))
		raw, err := hex.DecodeString(key)
		if err != nil || len(raw) != 32 {
			return "", errors.New("invalid Nostr private key")
		}
		var scalar secp256k1.ModNScalar
		if scalar.SetByteSlice(raw) || scalar.IsZero() {
			return "", errors.New("invalid Nostr private key")
		}

		return key, nil
	}
	key, err := read()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	key = nostr.GeneratePrivateKey()
	if key == "" {
		return "", errors.New("could not generate Nostr private key")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return read()
	}
	if err != nil {
		return "", err
	}
	if _, err = fmt.Fprintln(file, key); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if err = file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return key, nil
}

type Publisher struct {
	relayURL, key string
	queue         chan Signal
	dropped       atomic.Uint64
	decoder       *flight.Service
}

func New(relayURL, key string) *Publisher {
	return &Publisher{relayURL: relayURL, key: key, queue: make(chan Signal, 20000), decoder: flight.NewService()}
}
func (p *Publisher) Add(frame string, received time.Time) {
	decoded, err := p.decoder.ConsumeLineAt(frame, received)
	if err != nil {
		log.Printf("Nostr signal decode failed: %v", err)
		return
	}
	aircraft := Aircraft{ICAO: decoded.ICAO, Callsign: decoded.Callsign, AltitudeFeet: decoded.AltitudeFeet, SpeedKnots: decoded.SpeedKnots, HeadingDeg: decoded.HeadingDeg, VerticalFPM: decoded.VerticalFPM}
	if decoded.Latitude != nil && decoded.Longitude != nil && !decoded.PositionTime.IsZero() && decoded.PositionReference != "" {
		aircraft.Position = &Position{Latitude: *decoded.Latitude, Longitude: *decoded.Longitude, Timestamp: decoded.PositionTime, Reference: decoded.PositionReference}
	}
	select {
	case p.queue <- Signal{Frame: frame, Received: received, Aircraft: aircraft}:
	default:
		n := p.dropped.Add(1)
		if n == 1 || n%1000 == 0 {
			log.Printf("Nostr queue full: %d signals dropped", n)
		}
	}
}
func (p *Publisher) event(batch []Signal) (nostr.Event, error) {
	b, err := json.Marshal(Message{EventType, Version, batch})
	if err != nil {
		return nostr.Event{}, err
	}
	event := nostr.Event{CreatedAt: nostr.Now(), Kind: Kind, Tags: nostr.Tags{{"t", "trackflights-sdr"}, {"v", strconv.Itoa(Version)}}, Content: string(b)}
	err = event.Sign(p.key)
	return event, err
}

// Run retries a batch after connection or acknowledgement failures. The bounded
// queue prevents a stalled relay from blocking the receiver or exhausting memory.
func (p *Publisher) Run(ctx context.Context) {
	var relay *nostr.Relay
	defer func() {
		if relay != nil {
			relay.Close()
		}
	}()
	for {
		var first Signal
		select {
		case <-ctx.Done():
			return
		case first = <-p.queue:
		}
		batch := []Signal{first}
		timer := time.NewTimer(time.Second)
	collect:
		for len(batch) < 25 {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case signal := <-p.queue:
				batch = append(batch, signal)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		event, err := p.event(batch)
		if err != nil {
			log.Printf("Nostr signing failed: %v", err)
			continue
		}
		for ctx.Err() == nil {
			if relay == nil || !relay.IsConnected() {
				if relay != nil {
					relay.Close()
					relay = nil
				}
				c, cancel := context.WithTimeout(ctx, 5*time.Second)
				relay, err = nostr.RelayConnect(c, p.relayURL)
				cancel()
				if err != nil {
					relay = nil
					log.Printf("Nostr relay connection failed: %v", err)
				}
			}
			if relay != nil {
				c, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = relay.Publish(c, event)
				cancel()
				if err == nil {
					break
				}
				log.Printf("Nostr publish failed: %v", err)
				relay.Close()
				relay = nil
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}
