//go:build opencv

package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMergeSegmentTrackingResultsMergesBoundaryTracks(t *testing.T) {
	settings := DefaultTrackingSettings()
	manifest := RawSegmentManifest{
		CreatedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		Segments: []RawVideoSegment{
			{Index: 1, File: "segment_000001.avi", StartFrame: 1, EndFrame: 12, StartTimeMS: 0, EndTimeMS: 1100, Frames: 12, OverlapAfterFrames: 2},
			{Index: 2, File: "segment_000002.avi", StartFrame: 11, EndFrame: 22, StartTimeMS: 1000, EndTimeMS: 2100, Frames: 12, OverlapBeforeFrames: 2},
		},
	}

	left := EventMetadata{
		FPS:    10,
		Width:  1920,
		Height: 1080,
		Frames: []FrameMetadata{
			frameWithTrack(10, 900, trackAt(1, 100, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(11, 1000, trackAt(1, 110, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(12, 1100, trackAt(1, 120, 100, 20, 20, 10, 0, trackTypeFast)),
		},
	}
	right := EventMetadata{
		FPS:    10,
		Width:  1920,
		Height: 1080,
		Frames: []FrameMetadata{
			frameWithTrack(1, 0, trackAt(7, 110, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(2, 100, trackAt(7, 120, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(3, 200, trackAt(7, 130, 100, 20, 20, 10, 0, trackTypeFast)),
		},
	}

	merged, err := MergeSegmentTrackingResults(manifest, []EventMetadata{left, right}, settings)
	if err != nil {
		t.Fatalf("MergeSegmentTrackingResults returned error: %v", err)
	}

	if len(merged.BoundaryMatch) != 1 {
		t.Fatalf("expected one boundary match, got %d", len(merged.BoundaryMatch))
	}
	if merged.BoundaryMatch[0].LeftTrackID != 1 || merged.BoundaryMatch[0].RightTrackID != 7 {
		t.Fatalf("unexpected boundary match: %+v", merged.BoundaryMatch[0])
	}

	if len(merged.Metadata.Frames) != 4 {
		t.Fatalf("expected 4 merged frames after overlap collapse, got %d", len(merged.Metadata.Frames))
	}

	globalIDBySegmentTrack := make(map[[2]int]int)
	for _, assignment := range merged.Assignments {
		globalIDBySegmentTrack[[2]int{assignment.SegmentIndex, assignment.LocalTrackID}] = assignment.GlobalTrackID
	}
	if globalIDBySegmentTrack[[2]int{0, 1}] != globalIDBySegmentTrack[[2]int{1, 7}] {
		t.Fatalf("expected merged global IDs, assignments=%+v", merged.Assignments)
	}
}

func TestMergeSegmentTrackingResultsKeepsDistinctTracksSeparate(t *testing.T) {
	settings := DefaultTrackingSettings()
	manifest := RawSegmentManifest{
		CreatedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		Segments: []RawVideoSegment{
			{Index: 1, File: "segment_000001.avi", StartFrame: 1, EndFrame: 12, StartTimeMS: 0, EndTimeMS: 1100, Frames: 12, OverlapAfterFrames: 2},
			{Index: 2, File: "segment_000002.avi", StartFrame: 11, EndFrame: 22, StartTimeMS: 1000, EndTimeMS: 2100, Frames: 12, OverlapBeforeFrames: 2},
		},
	}

	left := EventMetadata{
		FPS:    10,
		Width:  1920,
		Height: 1080,
		Frames: []FrameMetadata{
			frameWithTrack(11, 1000, trackAt(1, 110, 100, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(12, 1100, trackAt(1, 120, 100, 20, 20, 10, 0, trackTypeFast)),
		},
	}
	right := EventMetadata{
		FPS:    10,
		Width:  1920,
		Height: 1080,
		Frames: []FrameMetadata{
			frameWithTrack(1, 0, trackAt(7, 600, 300, 20, 20, 10, 0, trackTypeFast)),
			frameWithTrack(2, 100, trackAt(7, 620, 300, 20, 20, 10, 0, trackTypeFast)),
		},
	}

	merged, err := MergeSegmentTrackingResults(manifest, []EventMetadata{left, right}, settings)
	if err != nil {
		t.Fatalf("MergeSegmentTrackingResults returned error: %v", err)
	}

	if len(merged.BoundaryMatch) != 0 {
		t.Fatalf("expected no boundary matches, got %+v", merged.BoundaryMatch)
	}
	if len(merged.Assignments) != 2 {
		t.Fatalf("expected two assignments, got %d", len(merged.Assignments))
	}
	if merged.Assignments[0].GlobalTrackID == merged.Assignments[1].GlobalTrackID {
		t.Fatalf("expected distinct global IDs, assignments=%+v", merged.Assignments)
	}
}

func TestMergeSegmentTrackingResultsPreservesCaptureAndPhotometry(t *testing.T) {
	startedAt := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	latitude := 59.3293
	capture := CaptureMetadata{
		Source:   "DWARF 3",
		Camera:   "wide",
		Latitude: &latitude,
	}
	manifest := RawSegmentManifest{
		CreatedAt: startedAt,
		Capture:   capture,
		Segments: []RawVideoSegment{
			{Index: 1, File: "segment_000001.avi", StartFrame: 1, EndFrame: 2, StartTimeMS: 0, EndTimeMS: 100, Frames: 2},
		},
	}
	segment := EventMetadata{
		EventID: "segment_event",
		FPS:     10,
		Width:   640,
		Height:  480,
		Frames: []FrameMetadata{
			{SourceFrame: 1, TimeMS: 0, MeanLuma: 10},
			{SourceFrame: 2, TimeMS: 100, MeanLuma: 20},
		},
	}

	merged, err := MergeSegmentTrackingResults(manifest, []EventMetadata{segment}, DefaultTrackingSettings())
	if err != nil {
		t.Fatalf("merge segment: %v", err)
	}
	if merged.Metadata.Capture.Source != capture.Source || merged.Metadata.Capture.Camera != capture.Camera {
		t.Fatalf("capture metadata was dropped: %+v", merged.Metadata.Capture)
	}
	if merged.Metadata.Capture.Latitude == nil || *merged.Metadata.Capture.Latitude != latitude {
		t.Fatalf("capture latitude was dropped: %+v", merged.Metadata.Capture)
	}
	if len(merged.Metadata.Frames) != 2 || merged.Metadata.Frames[0].MeanLuma != 10 || merged.Metadata.Frames[1].MeanLuma != 20 {
		t.Fatalf("frame luminance was dropped: %+v", merged.Metadata.Frames)
	}

	processedDir := t.TempDir()
	if err := writeMergedSegmentOutputs(processedDir, merged, DefaultTrackingSettings()); err != nil {
		t.Fatalf("write merged outputs: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(processedDir, "merged_event.json"))
	if err != nil {
		t.Fatalf("read merged summary: %v", err)
	}
	var summary EventSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("decode merged summary: %v", err)
	}
	if summary.Capture.Source != capture.Source || summary.Photometry.Samples != 2 || summary.Photometry.MeanLuma != 15 {
		t.Fatalf("merged summary omitted metadata: %+v", summary)
	}
}

func frameWithTrack(sourceFrame int, timeMS int64, track TrackMetadata) FrameMetadata {
	return FrameMetadata{
		SourceFrame: sourceFrame,
		TimeMS:      timeMS,
		Tracks:      []TrackMetadata{track},
	}
}

func trackAt(id, x, y, w, h int, vx, vy float64, trackType string) TrackMetadata {
	return TrackMetadata{
		ID:        id,
		Type:      trackType,
		X:         x,
		Y:         y,
		BoxX:      x - w/2,
		BoxY:      y - h/2,
		BoxWidth:  w,
		BoxHeight: h,
		VX:        vx,
		VY:        vy,
		Speed:     vx,
	}
}
