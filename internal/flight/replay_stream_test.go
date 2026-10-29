package flight

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jlambert/trackflights/internal/flight/tracking"
)

func TestStreamReplayMatchesReplayAndRespectsBackpressure(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	for aircraft, icao := range []string{"4AAB15", "4AB824"} {
		dir := filepath.Join(root, icao)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(dir, "2026-10-13.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 140; i++ {
			second := i*2 + aircraft
			if i == 30 {
				second += 4
			}
			if i == 31 {
				second -= 2
			}
			entry := tracking.Entry{Event: "adsb_frame", ICAO: icao, ReceivedAt: start.Add(time.Duration(second) * time.Second), Callsign: "TEST"}
			if err := json.NewEncoder(file).Encode(entry); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := file.WriteString("invalid json\n"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	want, err := ReadReplay(root, "2026-10-13")
	if err != nil {
		t.Fatal(err)
	}
	var header Replay
	var got []ReplayEvent
	batches := 0
	err = StreamReplay(context.Background(), root, "2026-10-13", Receiver{}, func(value Replay) error { header = value; return nil }, func(events []ReplayEvent) error {
		batches++
		if len(events) > 128 {
			t.Fatalf("oversized batch: %d", len(events))
		}
		got = append(got, events...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if batches < 2 || !reflect.DeepEqual(got, want.Events) || header.StartMilliseconds != want.StartMilliseconds || header.EndMilliseconds != want.EndMilliseconds || header.SkippedLines != want.SkippedLines {
		t.Fatalf("stream mismatch: batches=%d got=%d want=%d header=%+v", batches, len(got), len(want.Events), header)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := StreamReplay(ctx, root, "2026-10-13", Receiver{}, func(Replay) error { return nil }, func([]ReplayEvent) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestReplayWindowRetainsStateAndExcludesOtherTimes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "4AAB15")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "2026-10-13.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	for _, entry := range []tracking.Entry{{Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: start, Callsign: "FIRST"}, {Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: start.Add(4 * time.Minute), AltitudeFeet: intValue(1200)}, {Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: start.Add(6 * time.Minute)}, {Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: start.Add(15 * time.Minute)}} {
		if err := json.NewEncoder(file).Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	from := start.Add(5 * time.Minute).UnixMilli()
	var info Replay
	var events []ReplayEvent
	err = StreamReplayWindow(context.Background(), root, "2026-10-13", Receiver{}, &from, func(value Replay) error { info = value; return nil }, func(chunk []ReplayEvent) error { events = append(events, chunk...); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if info.WindowStartMilliseconds != from || info.WindowEndMilliseconds != start.Add(10*time.Minute).UnixMilli() || len(events) != 2 {
		t.Fatalf("wrong window: %+v events=%+v", info, events)
	}
	if events[0].Milliseconds != start.Add(4*time.Minute).UnixMilli() || events[1].Aircraft.Callsign != "FIRST" || events[1].Aircraft.AltitudeFeet == nil || *events[1].Aircraft.AltitudeFeet != 1200 {
		t.Fatalf("boundary state lost: %+v", events)
	}
}

func TestReplayWindowPreservesSnapshotAcrossEmptyWindow(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "4AAB15")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "2026-10-13.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	for _, minute := range []int{4, 15} {
		if err := json.NewEncoder(file).Encode(tracking.Entry{Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: start.Add(time.Duration(minute) * time.Minute), Callsign: "TEST"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	from := start.Add(5 * time.Minute).UnixMilli()
	var events []ReplayEvent
	if err := StreamReplayWindow(context.Background(), root, "2026-10-13", Receiver{}, &from, func(Replay) error { return nil }, func(batch []ReplayEvent) error { events = append(events, batch...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Aircraft.Callsign != "TEST" || events[0].Milliseconds != start.Add(4*time.Minute).UnixMilli() {
		t.Fatalf("expected boundary snapshot: %+v", events)
	}
}

func TestReplayWindowUsesConfiguredAircraftExpiry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "4AAB15")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "2026-10-13.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	for _, entry := range []tracking.Entry{{Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: start, Callsign: "FIRST"}, {Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: start.Add(8 * time.Minute)}} {
		if err := json.NewEncoder(file).Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	from := start.Add(7 * time.Minute).UnixMilli()
	for _, test := range []struct {
		expiry   time.Duration
		count    int
		callsign string
	}{{10 * time.Minute, 2, "FIRST"}, {2 * time.Minute, 1, ""}} {
		var info Replay
		var events []ReplayEvent
		err := StreamReplayWindow(context.Background(), root, "2026-10-13", Receiver{}, &from, func(value Replay) error { info = value; return nil }, func(batch []ReplayEvent) error { events = append(events, batch...); return nil }, test.expiry)
		if err != nil {
			t.Fatal(err)
		}
		if info.AircraftExpirySeconds != int64(test.expiry/time.Second) || len(events) != test.count || events[len(events)-1].Aircraft.Callsign != test.callsign {
			t.Fatalf("expiry %v: info=%+v events=%+v", test.expiry, info, events)
		}
	}
}

func TestReplayTracksStreamEarlierFixesOnlyForRequestedAircraft(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC)
	for _, id := range []string{"4AAB15", "4AB824"} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(filepath.Join(dir, "2026-10-13.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		// Deliberately out of order; repeated frames for the same fix must not
		// produce extra track points.
		for _, minute := range []int{6, 1, 3, 2} {
			positionMinute := minute
			if minute == 2 {
				positionMinute = 1
			}
			position := &tracking.Position{Latitude: 59 + float64(positionMinute)/100, Longitude: 18, Timestamp: start.Add(time.Duration(positionMinute) * time.Minute)}
			entry := tracking.Entry{Event: "adsb_frame", ICAO: id, ReceivedAt: start.Add(time.Duration(minute) * time.Minute), StoredPosition: position}
			if err := json.NewEncoder(file).Encode(entry); err != nil {
				t.Fatal(err)
			}
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var points []ReplayEvent
	err := StreamReplayTracks(context.Background(), root, "2026-10-13", Receiver{}, []string{"4AAB15"}, start.Add(5*time.Minute).UnixMilli(), func(batch []ReplayEvent) error { points = append(points, batch...); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Aircraft.ICAO != "4AAB15" || !points[0].Aircraft.PositionTimestamp.Equal(start.Add(time.Minute)) || !points[1].Aircraft.PositionTimestamp.Equal(start.Add(3*time.Minute)) {
		t.Fatalf("unexpected prior track points: %+v", points)
	}
}

func TestReplayExternalMergeMultipleRuns(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "4AAB15")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "2026-10-13.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	for i := replayRunSize + 5; i >= 0; i-- {
		if err := json.NewEncoder(file).Encode(tracking.Entry{Event: "adsb_frame", ICAO: "4AAB15", ReceivedAt: start.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	count := 0
	var previous int64
	err = StreamReplay(context.Background(), root, "2026-10-13", Receiver{}, func(Replay) error { return nil }, func(events []ReplayEvent) error {
		for _, event := range events {
			if event.Milliseconds < previous {
				t.Fatalf("events out of order")
			}
			previous = event.Milliseconds
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != replayRunSize+6 {
		t.Fatalf("got %d events", count)
	}
}
