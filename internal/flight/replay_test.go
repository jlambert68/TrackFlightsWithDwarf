package flight

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jlambert/trackflights/internal/flight/tracking"
)

func TestReadReplayRetainsHistoricalPositionAndMapPosition(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "4AAB15")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	entries := []tracking.Entry{
		{Event: "adsb_frame", ReceivedAt: first, ICAO: "4AAB15", Callsign: "TEST123", AltitudeFeet: intValue(1200), StoredPosition: &tracking.Position{Latitude: 59.33, Longitude: 18.48, Timestamp: first}},
		{Event: "map_position_set", ReceivedAt: first.Add(time.Second), ICAO: "4AAB15", WorldPosition: &tracking.WorldPosition{SetAt: first.Add(time.Second), Latitude: 59.34, Longitude: 18.49, AltitudeFeet: 1250}},
	}
	file, err := os.Create(filepath.Join(dir, "2026-10-13.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := json.NewEncoder(file).Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	replay, err := ReadReplay(root, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 2 || replay.StartMilliseconds != first.UnixMilli() || replay.EndMilliseconds != first.Add(time.Second).UnixMilli() {
		t.Fatalf("unexpected replay: %+v", replay)
	}
	last := replay.Events[1].Aircraft
	if last.Callsign != "TEST123" || last.Latitude == nil || *last.Latitude != 59.34 || last.AltitudeFeet == nil || *last.AltitudeFeet != 1250 {
		t.Fatalf("unexpected replay aircraft: %+v", last)
	}
}

func intValue(v int) *int { return &v }

func TestReadReplayDecodesLegacyRawPositionPair(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "40621D")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "2026-10-13.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	for i, raw := range []string{"8D40621D58C382D690C8AC2863A7", "8D40621D58C386435CC412692AD6"} {
		if err := json.NewEncoder(file).Encode(tracking.Entry{Event: "adsb_frame", ICAO: "40621D", ReceivedAt: first.Add(time.Duration(i) * time.Second), RawFrame: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := ReadReplay(root, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	last := replay.Events[len(replay.Events)-1].Aircraft
	if last.Latitude == nil || last.Longitude == nil || *last.Latitude < 51 || *last.Latitude > 54 || *last.Longitude < 2 || *last.Longitude > 5 {
		t.Fatalf("position pair not decoded: %+v", last)
	}
}

func TestReplaySkipsDuplicateWorldPosition(t *testing.T) {
	when := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	decoder := newReplayDecoder(Receiver{})
	_, ok := decoder.apply(tracking.Entry{Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: when, AltitudeFeet: intValue(1200), StoredPosition: &tracking.Position{Latitude: 59.3, Longitude: 18.4, Timestamp: when}})
	if !ok {
		t.Fatal("missing received position")
	}
	_, ok = decoder.apply(tracking.Entry{Event: "map_position_set", ICAO: "4AAB15", ReceivedAt: when.Add(time.Second), WorldPosition: &tracking.WorldPosition{SetAt: when.Add(time.Second), Latitude: 59.3, Longitude: 18.4, AltitudeFeet: 1200}})
	if ok {
		t.Fatal("duplicate browser position must not create a second receiver fix")
	}
	if !decoder.last["4AAB15"].Equal(when.Add(time.Second)) {
		t.Fatal("duplicate event should still keep the aircraft active")
	}
}
