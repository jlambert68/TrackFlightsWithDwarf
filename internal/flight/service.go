package flight

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jlambert/trackflights/internal/flight/adsb"
)

// Receiver identifies the configured position of the ADS-B receiver.
type Receiver struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Aircraft is the stable browser representation of a live ADS-B target.
type Aircraft struct {
	ICAO              string    `json:"icao"`
	Raw               string    `json:"raw"`
	Frames            int       `json:"frames"`
	DownlinkFormat    int       `json:"downlinkFormat"`
	MessageType       int       `json:"messageType"`
	PositionReady     bool      `json:"positionReady"`
	Callsign          string    `json:"callsign,omitempty"`
	AltitudeFeet      *int      `json:"altitudeFeet,omitempty"`
	SpeedKnots        *float64  `json:"speedKnots,omitempty"`
	HeadingDeg        *float64  `json:"headingDeg,omitempty"`
	VerticalFPM       *int      `json:"verticalFPM,omitempty"`
	Latitude          *float64  `json:"latitude,omitempty"`
	Longitude         *float64  `json:"longitude,omitempty"`
	PositionTimestamp time.Time `json:"positionTimestamp,omitempty"`
	PositionReference string    `json:"positionReference,omitempty"`
	LastSeen          time.Time `json:"lastSeen"`
}

type LiveState struct {
	Receiver      Receiver   `json:"receiver"`
	Aircraft      []Aircraft `json:"aircraft"`
	LiveAvailable bool       `json:"liveAvailable"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// Service owns mutable receiver state. A receiver process or a fixture reader
// supplies frames through ConsumeLine; HTTP handlers only observe snapshots.
type Service struct {
	mu            sync.RWMutex
	flights       map[string]adsb.Message
	cpr           map[string][2]*cprValue
	receiver      Receiver
	liveAvailable bool
	updated       time.Time
	expiry        time.Duration
	updates       chan struct{}
}

func NewService(receiver ...Receiver) *Service {
	r := Receiver{}
	if len(receiver) > 0 {
		r = receiver[0]
	}
	return &Service{flights: make(map[string]adsb.Message), cpr: make(map[string][2]*cprValue), receiver: r, expiry: aircraftExpiry, updates: make(chan struct{}, 1)}
}

func (s *Service) SetAircraftExpiry(expiry time.Duration) {
	s.mu.Lock()
	s.expiry = normalizedAircraftExpiry(expiry)
	s.mu.Unlock()
	s.notify()
}

// SetReceiver updates the home used by the map and future local CPR decoding.
func (s *Service) SetReceiver(receiver Receiver) {
	s.mu.Lock()
	s.receiver = receiver
	s.mu.Unlock()
	s.notify()
}

func (s *Service) ConsumeLine(line string) (adsb.Message, error) {
	message, err := adsb.DecodeLine(line)
	if err != nil {
		return adsb.Message{}, err
	}
	message.LastSeen = time.Now().UTC()
	s.mu.Lock()
	previous := s.flights[message.ICAO]
	if !previous.LastSeen.IsZero() && message.LastSeen.Sub(previous.LastSeen) > s.expiry {
		delete(s.cpr, message.ICAO)
		previous = adsb.Message{}
	}
	if message.HasCPR {
		reference := cprDecodeReference{latitude: 999, longitude: 999}
		if previous.Latitude != nil && previous.Longitude != nil {
			reference = cprDecodeReference{latitude: *previous.Latitude, longitude: *previous.Longitude, source: "aircraft"}
		} else if s.receiver.Latitude != 0 || s.receiver.Longitude != 0 {
			reference = cprDecodeReference{latitude: s.receiver.Latitude, longitude: s.receiver.Longitude, source: "receiver"}
		}
		updateCPR(&message, s.cpr, message.LastSeen, reference)
		if message.Latitude != nil && previous.Latitude != nil && previous.Longitude != nil && !previous.PositionTime.IsZero() {
			distance := distanceMeters(*previous.Latitude, *previous.Longitude, *message.Latitude, *message.Longitude)
			elapsed := message.PositionTime.Sub(previous.PositionTime).Seconds()
			if elapsed <= 0 || distance > 100000 || (distance > 250 && distance/elapsed*1.9438444924406 > 1500) {
				message.Latitude, message.Longitude = nil, nil
				message.PositionTime = time.Time{}
				message.PositionReference = ""
			}
		}
	}
	message.Frames = previous.Frames + 1
	message.Callsign = first(message.Callsign, previous.Callsign)
	if message.AltitudeFeet == nil {
		message.AltitudeFeet = previous.AltitudeFeet
	}
	if message.SpeedKnots == nil {
		message.SpeedKnots = previous.SpeedKnots
	}
	if message.HeadingDeg == nil {
		message.HeadingDeg = previous.HeadingDeg
	}
	if message.VerticalFPM == nil {
		message.VerticalFPM = previous.VerticalFPM
	}
	if message.Latitude == nil {
		message.Latitude, message.Longitude = previous.Latitude, previous.Longitude
		message.PositionTime, message.PositionReference = previous.PositionTime, previous.PositionReference
	}
	s.flights[message.ICAO] = message
	s.liveAvailable, s.updated = true, message.LastSeen
	s.expireLocked(message.LastSeen)
	s.mu.Unlock()
	s.notify()
	return message, nil
}

func (s *Service) Consume(ctx context.Context, reader io.Reader) error {
	buffer := make([]byte, 0, 4096)
	one := make([]byte, 1)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := reader.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				if _, decodeErr := s.ConsumeLine(string(buffer)); decodeErr != nil && strings.TrimSpace(string(buffer)) != "" {
					return decodeErr
				}
				buffer = buffer[:0]
			} else {
				buffer = append(buffer, one[0])
			}
		}
		if err == io.EOF {
			if len(buffer) > 0 {
				_, _ = s.ConsumeLine(string(buffer))
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("read ADS-B input: %w", err)
		}
	}
}

func (s *Service) Snapshot() []adsb.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now().UTC())
	if !s.updated.IsZero() && time.Since(s.updated) > s.expiry {
		s.liveAvailable = false
	}
	items := make([]adsb.Message, 0, len(s.flights))
	for _, flight := range s.flights {
		items = append(items, flight)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ICAO < items[j].ICAO })
	return items
}

func (s *Service) LiveState() LiveState {
	items := s.Snapshot()
	s.mu.RLock()
	state := LiveState{Receiver: s.receiver, LiveAvailable: s.liveAvailable, UpdatedAt: s.updated, Aircraft: make([]Aircraft, 0, len(items))}
	s.mu.RUnlock()
	for _, message := range items {
		state.Aircraft = append(state.Aircraft, aircraftFromMessage(message))
	}
	return state
}

// Updates is a coalescing notification channel. Consumers read a fresh
// snapshot rather than treating notifications as a message queue.
func (s *Service) Updates() <-chan struct{} { return s.updates }
func (s *Service) notify() {
	select {
	case s.updates <- struct{}{}:
	default:
	}
}
func (s *Service) expireLocked(now time.Time) {
	for icao, item := range s.flights {
		if item.LastSeen.IsZero() || now.Sub(item.LastSeen) > s.expiry {
			delete(s.flights, icao)
			delete(s.cpr, icao)
		}
	}
}
func aircraftFromMessage(m adsb.Message) Aircraft {
	return Aircraft{ICAO: m.ICAO, Raw: m.Raw, Frames: m.Frames, DownlinkFormat: m.DownlinkFormat, MessageType: m.TypeCode, PositionReady: m.Latitude != nil && m.Longitude != nil, Callsign: m.Callsign, AltitudeFeet: m.AltitudeFeet, SpeedKnots: m.SpeedKnots, HeadingDeg: m.HeadingDeg, VerticalFPM: m.VerticalFPM, Latitude: m.Latitude, Longitude: m.Longitude, PositionTimestamp: m.PositionTime, PositionReference: m.PositionReference, LastSeen: m.LastSeen}
}
func first(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
