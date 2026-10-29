package flight

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jlambert/trackflights/internal/flight/adsb"
	"github.com/jlambert/trackflights/internal/flight/tracking"
)

const aircraftExpiry = 5 * time.Minute

func normalizedAircraftExpiry(expiry ...time.Duration) time.Duration {
	if len(expiry) > 0 && expiry[0] > 0 {
		return expiry[0]
	}
	return aircraftExpiry
}

type ReplayAircraft struct {
	ICAO               string    `json:"icao"`
	Callsign           string    `json:"callsign,omitempty"`
	Registration       string    `json:"registration,omitempty"`
	DepartureAirport   string    `json:"departureAirport,omitempty"`
	DestinationAirport string    `json:"destinationAirport,omitempty"`
	Raw                string    `json:"raw,omitempty"`
	DownlinkFormat     int       `json:"downlinkFormat,omitempty"`
	MessageType        int       `json:"messageType,omitempty"`
	VerticalFPM        *int      `json:"verticalFPM,omitempty"`
	PositionReference  string    `json:"positionReference,omitempty"`
	AltitudeFeet       *int      `json:"altitudeFeet,omitempty"`
	SpeedKnots         *float64  `json:"speedKnots,omitempty"`
	HeadingDeg         *float64  `json:"headingDeg,omitempty"`
	Latitude           *float64  `json:"latitude,omitempty"`
	Longitude          *float64  `json:"longitude,omitempty"`
	PositionTimestamp  time.Time `json:"positionTimestamp,omitempty"`
	Frames             int       `json:"frames"`
}
type ReplayEvent struct {
	Timestamp    time.Time      `json:"timestamp"`
	Milliseconds int64          `json:"milliseconds"`
	Aircraft     ReplayAircraft `json:"aircraft"`
}
type Replay struct {
	Date                    string        `json:"date"`
	StartMilliseconds       int64         `json:"startMilliseconds"`
	EndMilliseconds         int64         `json:"endMilliseconds"`
	WindowStartMilliseconds int64         `json:"windowStartMilliseconds,omitempty"`
	WindowEndMilliseconds   int64         `json:"windowEndMilliseconds,omitempty"`
	AircraftExpirySeconds   int64         `json:"aircraftExpirySeconds"`
	Events                  []ReplayEvent `json:"events"`
	SkippedLines            int           `json:"skippedLines"`
}
type replayEntry struct {
	entry    tracking.Entry
	sequence int
}

func ReplayDates(root string) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return []string{}, nil
	}
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	dates := map[string]bool{}
	for _, dir := range dirs {
		if !dir.IsDir() || !validICAO(dir.Name()) {
			continue
		}
		files, e := os.ReadDir(filepath.Join(root, dir.Name()))
		if e != nil {
			return nil, e
		}
		for _, f := range files {
			d := strings.TrimSuffix(f.Name(), ".jsonl")
			if !f.IsDir() && validDate(d) {
				dates[d] = true
			}
		}
	}
	result := make([]string, 0, len(dates))
	for d := range dates {
		result = append(result, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(result)))
	return result, nil
}
func ReadReplay(root, date string) (Replay, error) {
	return ReadReplayWithReceiver(root, date, Receiver{})
}

func ReadReplayWithReceiver(root, date string, receiver Receiver, expiry ...time.Duration) (Replay, error) {
	if !validDate(date) {
		return Replay{}, errors.New("invalid replay date")
	}
	duration := normalizedAircraftExpiry(expiry...)
	result := Replay{Date: date, AircraftExpirySeconds: int64(duration / time.Second), Events: []ReplayEvent{}}
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	entries := []replayEntry{}
	seq := 0
	for _, dir := range dirs {
		if !dir.IsDir() || !validICAO(dir.Name()) {
			continue
		}
		file, e := os.Open(filepath.Join(root, dir.Name(), date+".jsonl"))
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return result, e
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			var entry tracking.Entry
			if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.ReceivedAt.IsZero() {
				result.SkippedLines++
				continue
			}
			if entry.ICAO == "" {
				entry.ICAO = strings.ToUpper(dir.Name())
			}
			entry.ICAO = strings.ToUpper(entry.ICAO)
			if !validICAO(entry.ICAO) {
				result.SkippedLines++
				continue
			}
			entries = append(entries, replayEntry{entry, seq})
			seq++
		}
		if e := scanner.Err(); e != nil {
			_ = file.Close()
			return result, e
		}
		_ = file.Close()
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].entry.ReceivedAt.Equal(entries[j].entry.ReceivedAt) {
			return entries[i].sequence < entries[j].sequence
		}
		return entries[i].entry.ReceivedAt.Before(entries[j].entry.ReceivedAt)
	})
	decoder := newReplayDecoder(receiver, duration)
	for _, record := range entries {
		if event, ok := decoder.apply(record.entry); ok {
			result.Events = append(result.Events, event)
		}
	}
	if len(result.Events) > 0 {
		result.StartMilliseconds = result.Events[0].Milliseconds
		result.EndMilliseconds = result.Events[len(result.Events)-1].Milliseconds
	}
	return result, nil
}

type replayDecoder struct {
	states    map[string]ReplayAircraft
	last      map[string]time.Time
	cprStates map[string][2]*cprValue
	receiver  Receiver
	expiry    time.Duration
}

func newReplayDecoder(receiver Receiver, expiry ...time.Duration) *replayDecoder {
	return &replayDecoder{states: map[string]ReplayAircraft{}, last: map[string]time.Time{}, cprStates: map[string][2]*cprValue{}, receiver: receiver, expiry: normalizedAircraftExpiry(expiry...)}
}
func (d *replayDecoder) apply(e tracking.Entry) (ReplayEvent, bool) {
	states, last, cprStates, receiver := d.states, d.last, d.cprStates, d.receiver
	if previous := last[e.ICAO]; !previous.IsZero() && e.ReceivedAt.Sub(previous) > d.expiry {
		delete(states, e.ICAO)
		delete(cprStates, e.ICAO)
	}
	a := states[e.ICAO]
	a.ICAO = e.ICAO
	if e.Event != "" && e.Event != "adsb_frame" && e.Event != "map_position_set" {
		return ReplayEvent{}, false
	}
	if e.Event == "map_position_set" && e.WorldPosition == nil {
		return ReplayEvent{}, false
	}
	if e.Event == "map_position_set" {
		if a.Latitude != nil && a.Longitude != nil && *a.Latitude == e.WorldPosition.Latitude && *a.Longitude == e.WorldPosition.Longitude && a.AltitudeFeet != nil && float64(*a.AltitudeFeet) == e.WorldPosition.AltitudeFeet {
			last[e.ICAO] = e.ReceivedAt
			return ReplayEvent{}, false
		}
		lat, lon := e.WorldPosition.Latitude, e.WorldPosition.Longitude
		a.Latitude = &lat
		a.Longitude = &lon
		altitude := int(e.WorldPosition.AltitudeFeet)
		a.AltitudeFeet = &altitude
		a.PositionTimestamp = e.WorldPosition.SetAt
		a.PositionReference = e.WorldPosition.CPRReference
	} else {
		a.Frames++
		a.Raw, a.DownlinkFormat, a.MessageType = e.RawFrame, e.DownlinkFormat, e.TypeCode
		if e.DepartureAirport != "" {
			a.DepartureAirport = e.DepartureAirport
		}
		if e.DestinationAirport != "" {
			a.DestinationAirport = e.DestinationAirport
		}
		if decoded, err := adsb.DecodeLine(e.RawFrame); err == nil && decoded.ICAO == e.ICAO {
			if decoded.HasCPR && e.StoredPosition == nil && e.PositionDecision != "rejected" {
				reference := cprDecodeReference{latitude: 999, longitude: 999}
				if a.Latitude != nil && a.Longitude != nil {
					reference = cprDecodeReference{latitude: *a.Latitude, longitude: *a.Longitude, source: "aircraft"}
				} else if receiver.Latitude != 0 || receiver.Longitude != 0 {
					reference = cprDecodeReference{latitude: receiver.Latitude, longitude: receiver.Longitude, source: "receiver"}
				}
				updateCPR(&decoded, cprStates, e.ReceivedAt, reference)
				if decoded.Latitude != nil && decoded.Longitude != nil {
					valid := true
					if a.Latitude != nil && a.Longitude != nil && !a.PositionTimestamp.IsZero() {
						distance := distanceMeters(*a.Latitude, *a.Longitude, *decoded.Latitude, *decoded.Longitude)
						elapsed := decoded.PositionTime.Sub(a.PositionTimestamp).Seconds()
						valid = elapsed > 0 && distance <= 100000 && (distance <= 250 || distance/elapsed*1.9438444924406 <= 1500)
					}
					if valid {
						a.Latitude, a.Longitude, a.PositionTimestamp, a.PositionReference = decoded.Latitude, decoded.Longitude, decoded.PositionTime, decoded.PositionReference
					}
				}
			}
			if a.Callsign == "" {
				a.Callsign = decoded.Callsign
			}
			if e.AltitudeFeet == nil && decoded.AltitudeFeet != nil {
				a.AltitudeFeet = decoded.AltitudeFeet
			}
			if e.ReportedSpeedKnots == nil && decoded.SpeedKnots != nil {
				a.SpeedKnots = decoded.SpeedKnots
			}
			if e.HeadingDegrees == nil && decoded.HeadingDeg != nil {
				a.HeadingDeg = decoded.HeadingDeg
			}
			if decoded.VerticalFPM != nil {
				a.VerticalFPM = decoded.VerticalFPM
			}
		}
		if e.Callsign != "" {
			a.Callsign = e.Callsign
		}
		if e.Registration != "" {
			a.Registration = e.Registration
		}
		if e.AltitudeFeet != nil {
			v := *e.AltitudeFeet
			a.AltitudeFeet = &v
		}
		if e.ReportedSpeedKnots != nil {
			v := *e.ReportedSpeedKnots
			a.SpeedKnots = &v
		}
		if e.HeadingDegrees != nil {
			v := *e.HeadingDegrees
			a.HeadingDeg = &v
		}
		if e.StoredPosition != nil {
			lat, lon := e.StoredPosition.Latitude, e.StoredPosition.Longitude
			a.Latitude = &lat
			a.Longitude = &lon
			a.PositionTimestamp = e.StoredPosition.Timestamp
			a.PositionReference = e.CPRReference
		}
	}
	states[e.ICAO] = a
	last[e.ICAO] = e.ReceivedAt
	return ReplayEvent{Timestamp: e.ReceivedAt, Milliseconds: e.ReceivedAt.UnixMilli(), Aircraft: a}, true
}

func validDate(v string) bool {
	p, e := time.Parse("2006-01-02", v)
	return e == nil && p.Format("2006-01-02") == v
}
func validICAO(v string) bool {
	if len(v) != 6 {
		return false
	}
	for _, r := range strings.ToUpper(v) {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return false
		}
	}
	return true
}
