//go:build opencv

package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceFrameTimestampUsesVideoTimeline(t *testing.T) {
	anchor := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	observedAt := anchor.Add(3 * time.Minute)

	got := sourceFrameTimestamp("video file", anchor, observedAt, 610, 30)
	want := anchor.Add(20*time.Second + 300*time.Millisecond)
	if !got.Equal(want) {
		t.Fatalf("unexpected timestamp for frame 610: got %s want %s", got, want)
	}
}

func TestSourceFrameTimestampUsesObservedTimeForLiveStream(t *testing.T) {
	anchor := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	observedAt := anchor.Add(7 * time.Second)

	got := sourceFrameTimestamp("DWARF 3 live stream", anchor, observedAt, 610, 30)
	if !got.Equal(observedAt) {
		t.Fatalf("live timestamp should use observed time: got %s want %s", got, observedAt)
	}
}

func TestEffectiveSettingsKeepConfiguredLiveEventBuffers(t *testing.T) {
	settings := DefaultTrackingSettings()
	settings.PreEventDuration = 750 * time.Millisecond
	settings.PostEventDuration = 1250 * time.Millisecond
	engine := TrackerEngine{Config: TrackerConfig{
		InputLabel: "DWARF 3 live stream",
		Settings:   settings,
	}}

	got := engine.effectiveSettings()
	if got.PreEventDuration != settings.PreEventDuration || got.PostEventDuration != settings.PostEventDuration {
		t.Fatalf("live buffers changed: got pre=%s post=%s", got.PreEventDuration, got.PostEventDuration)
	}
}

func TestFinishTrackerRunResourcesFinalizesOpenEventAfterError(t *testing.T) {
	dir := t.TempDir()
	startedAt := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(2 * time.Second)
	recorder := &EventRecorder{
		Directory:     dir,
		StartedAt:     startedAt,
		EventID:       "event",
		FPS:           30,
		Width:         16,
		Height:        16,
		SeenIDs:       make(map[int]struct{}),
		Settings:      DefaultTrackingSettings(),
		FramesWritten: 1,
		LumaSum:       12,
		MinLuma:       12,
		MaxLuma:       12,
	}
	if err := recorder.openTrackingStream(); err != nil {
		t.Fatalf("open tracking stream: %v", err)
	}
	if err := recorder.appendTrackingFrame(FrameMetadata{SourceFrame: 1, TimeUnixNS: startedAt.UnixNano(), MeanLuma: 12}); err != nil {
		t.Fatalf("append tracking frame: %v", err)
	}
	spooledPath := filepath.Join(dir, "spooled.jpg")
	if err := os.WriteFile(spooledPath, []byte("frame"), 0o600); err != nil {
		t.Fatalf("write spool fixture: %v", err)
	}

	if err := finishTrackerRunResources([]BufferedFrame{{ImagePath: spooledPath}}, recorder, nil, endedAt); err != nil {
		t.Fatalf("finish resources: %v", err)
	}
	if _, err := os.Stat(spooledPath); !os.IsNotExist(err) {
		t.Fatalf("spooled frame was not removed, stat error=%v", err)
	}

	eventData, err := os.ReadFile(filepath.Join(dir, "event.json"))
	if err != nil {
		t.Fatalf("read finalized event summary: %v", err)
	}
	var summary EventSummary
	if err := json.Unmarshal(eventData, &summary); err != nil {
		t.Fatalf("decode finalized event summary: %v", err)
	}
	if !summary.EndedAt.Equal(endedAt) || summary.DurationSeconds != 2 {
		t.Fatalf("unexpected finalized event timing: %+v", summary)
	}
	if summary.OriginalVideo != "" || summary.MaskedVideo != "" || summary.TrackedVideo != "tracked.avi" {
		t.Fatalf("unexpected optional event outputs: %+v", summary)
	}
	trackingData, err := os.ReadFile(filepath.Join(dir, "tracking.json"))
	if err != nil {
		t.Fatalf("read finalized tracking metadata: %v", err)
	}
	var metadata EventMetadata
	if err := json.Unmarshal(trackingData, &metadata); err != nil {
		t.Fatalf("tracking metadata was not finalized as valid JSON: %v", err)
	}
	if len(metadata.Frames) != 1 {
		t.Fatalf("unexpected finalized tracking frames: %+v", metadata.Frames)
	}
}
