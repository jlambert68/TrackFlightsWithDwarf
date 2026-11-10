//go:build opencv

package engine

import (
	"image"
	"testing"
)

func TestScaledFrameSizeDownsizesWithoutChangingAspectRatio(t *testing.T) {
	width, height, scale := scaledFrameSize(3840, 2160, 1920)
	if width != 1920 || height != 1080 || scale != 0.5 {
		t.Fatalf("unexpected analysis size: %dx%d scale=%v", width, height, scale)
	}

	width, height, scale = scaledFrameSize(1280, 720, 1920)
	if width != 1280 || height != 720 || scale != 1 {
		t.Fatalf("smaller source should not be enlarged: %dx%d scale=%v", width, height, scale)
	}
}

func TestTrackingSettingsForScalePreservesSourcePixelSemantics(t *testing.T) {
	settings := DefaultTrackingSettings()
	scaled := trackingSettingsForScale(settings, 0.5)

	if scaled.MinArea != settings.MinArea*0.25 || scaled.MaxArea != settings.MaxArea*0.25 {
		t.Fatalf("area thresholds were not scaled by image area: %+v", scaled)
	}
	if scaled.MaxMatchDistance != settings.MaxMatchDistance*0.5 {
		t.Fatalf("match distance was not scaled: %v", scaled.MaxMatchDistance)
	}
	if settings.MinArea != DefaultTrackingSettings().MinArea {
		t.Fatal("source settings were mutated")
	}
}

func TestScaleDetectionsRestoresSourceCoordinates(t *testing.T) {
	detections := []Detection{{
		Rect:   image.Rect(10, 20, 30, 40),
		Center: image.Pt(20, 30),
		Area:   100,
	}}

	got := scaleDetections(detections, 2)
	if got[0].Rect != image.Rect(20, 40, 60, 80) || got[0].Center != image.Pt(40, 60) || got[0].Area != 400 {
		t.Fatalf("unexpected source detection: %+v", got[0])
	}
}

func TestScaleTrackMetadataKeepsReportedSourceSpeed(t *testing.T) {
	tracks := []TrackMetadata{{
		X: 100, Y: 50,
		BoxX: 90, BoxY: 40, BoxWidth: 20, BoxHeight: 20,
		VX: 80, VY: 20, Speed: 82.46,
		Trail: []TrailPoint{{X: 80, Y: 45}, {X: 100, Y: 50}},
	}}

	got := scaleTrackMetadata(tracks, 0.5)[0]
	if got.X != 50 || got.Y != 25 || got.BoxWidth != 10 || got.VX != 40 || got.VY != 10 {
		t.Fatalf("unexpected preview track coordinates: %+v", got)
	}
	if got.Speed != tracks[0].Speed {
		t.Fatalf("preview changed the displayed source speed: got %v want %v", got.Speed, tracks[0].Speed)
	}
}

func TestPreviewFrameDueUsesSourceTimeline(t *testing.T) {
	due := make([]int, 0)
	for frame := 1; frame <= 30; frame++ {
		if previewFrameDue(frame, 30, 10) {
			due = append(due, frame)
		}
	}
	if len(due) != 10 || due[0] != 1 || due[len(due)-1] != 28 {
		t.Fatalf("unexpected preview cadence: %v", due)
	}
	if previewFrameDue(1, 30, 0) {
		t.Fatal("disabled preview emitted a frame")
	}
}
