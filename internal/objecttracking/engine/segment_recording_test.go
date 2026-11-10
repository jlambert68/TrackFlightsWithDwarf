//go:build opencv

package engine

import (
	"testing"
	"time"

	"gocv.io/x/gocv"
)

func TestRawSegmentRecorderDoesNotCreateOverlapOnlyFinalSegment(t *testing.T) {
	settings := DefaultTrackingSettings()
	settings.RawSegmentDuration = 2 * time.Second
	settings.RawSegmentOverlap = time.Second
	recorder, err := startRawSegmentRecorder(t.TempDir(), 2, 16, 16, settings, CaptureMetadata{})
	if err != nil {
		t.Fatalf("start recorder: %v", err)
	}

	frame := gocv.NewMatWithSize(16, 16, gocv.MatTypeCV8UC3)
	defer frame.Close()
	for sourceFrame := 1; sourceFrame <= 4; sourceFrame++ {
		offset := time.Duration(sourceFrame-1) * 500 * time.Millisecond
		if err := recorder.RecordFrame(frame, sourceFrame, offset); err != nil {
			t.Fatalf("record frame %d: %v", sourceFrame, err)
		}
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close recorder: %v", err)
	}

	if got := len(recorder.Manifest.Segments); got != 1 {
		t.Fatalf("expected one exact-length segment, got %d: %+v", got, recorder.Manifest.Segments)
	}
	segment := recorder.Manifest.Segments[0]
	if segment.Frames != 4 || segment.StartFrame != 1 || segment.EndFrame != 4 {
		t.Fatalf("unexpected segment bounds: %+v", segment)
	}
	if segment.OverlapAfterFrames != 0 {
		t.Fatalf("last segment claims overlap with a nonexistent successor: %+v", segment)
	}
}
