//go:build opencv

package engine

import (
	"image"
	"math"
	"testing"
	"time"
)

func TestDefaultGeneralTrackingSettingsAreHardened(t *testing.T) {
	settings := DefaultTrackingSettings()
	if settings.MinArea != 10.0 {
		t.Fatalf("unexpected MinArea: %v", settings.MinArea)
	}
	if settings.SlowMinSpeed != 14.0 {
		t.Fatalf("unexpected SlowMinSpeed: %v", settings.SlowMinSpeed)
	}
	if settings.MinSpeed != 55.0 {
		t.Fatalf("unexpected MinSpeed: %v", settings.MinSpeed)
	}
	if settings.MinHits != 3 {
		t.Fatalf("unexpected MinHits: %d", settings.MinHits)
	}
	if settings.ForegroundThreshold != 220.0 {
		t.Fatalf("unexpected ForegroundThreshold: %v", settings.ForegroundThreshold)
	}
	if settings.MOG2VarThreshold != 24.0 {
		t.Fatalf("unexpected MOG2VarThreshold: %v", settings.MOG2VarThreshold)
	}
}

func TestClassifyTrackRejectsJitterWithoutMeaningfulTravel(t *testing.T) {
	settings := DefaultTrackingSettings()
	track := &Track{
		Hits:  5,
		Speed: 70.0,
		Trail: []image.Point{
			{X: 100, Y: 100},
			{X: 103, Y: 101},
			{X: 101, Y: 99},
			{X: 104, Y: 100},
			{X: 102, Y: 101},
		},
	}

	if trackType, ok := classifyTrack(track, settings); ok {
		t.Fatalf("expected jitter track to be rejected, got type=%s", trackType)
	}
}

func TestClassifyTrackAcceptsRealMotion(t *testing.T) {
	settings := DefaultTrackingSettings()
	track := &Track{
		Hits:  5,
		Speed: 70.0,
		Trail: []image.Point{
			{X: 100, Y: 100},
			{X: 108, Y: 101},
			{X: 116, Y: 103},
			{X: 125, Y: 104},
			{X: 134, Y: 106},
		},
	}

	trackType, ok := classifyTrack(track, settings)
	if !ok {
		t.Fatal("expected real motion track to be accepted")
	}
	if trackType != trackTypeFast {
		t.Fatalf("expected fast track, got %s", trackType)
	}
}

func TestClassifyTrackHonorsTwoHitSetting(t *testing.T) {
	settings := DefaultTrackingSettings()
	settings.MinHits = 2
	track := &Track{
		Hits:  2,
		Speed: 900,
		Trail: []image.Point{
			{X: 100, Y: 500},
			{X: 125, Y: 470},
		},
	}

	trackType, ok := classifyTrack(track, settings)
	if !ok {
		t.Fatal("expected a two-frame fast track to be accepted when MinHits is 2")
	}
	if trackType != trackTypeFast {
		t.Fatalf("expected fast track, got %s", trackType)
	}
}

func TestScoreTrackDetectionMatchAllowsMotionBlurScaleChange(t *testing.T) {
	settings := DefaultTrackingSettings()
	track := &Track{
		Rect: image.Rect(80, 80, 120, 120),
		Hits: settings.MinHits * 2,
	}
	detection := Detection{
		Rect:   image.Rect(75, 55, 175, 155),
		Center: image.Pt(125, 105),
	}

	if _, ok := scoreTrackDetectionMatch(track, detection, 100, 100, settings); !ok {
		t.Fatal("expected a nearby motion-blurred detection to tolerate a linear scale change")
	}
}

func TestTrackDetectionMatchDistanceScalesWithObjectSize(t *testing.T) {
	settings := DefaultTrackingSettings()
	largeTrack := &Track{
		Rect:     image.Rect(60, 60, 140, 140),
		Position: image.Pt(100, 100),
	}
	largeDetection := Detection{
		Rect:   image.Rect(190, 60, 270, 140),
		Center: image.Pt(230, 100),
	}
	if _, ok := scoreTrackDetectionMatch(largeTrack, largeDetection, 100, 100, settings); !ok {
		t.Fatal("expected a large object to receive a size-aware match allowance")
	}

	smallTrack := &Track{
		Rect:     image.Rect(96, 96, 104, 104),
		Position: image.Pt(100, 100),
	}
	smallDetection := Detection{
		Rect:   image.Rect(226, 96, 234, 104),
		Center: image.Pt(230, 100),
	}
	if _, ok := scoreTrackDetectionMatch(smallTrack, smallDetection, 100, 100, settings); ok {
		t.Fatal("expected the same jump to remain out of range for a small object")
	}
}

func TestLargeFastObjectBecomesVisibleAfterConfiguredTwoHits(t *testing.T) {
	settings := DefaultTrackingSettings()
	settings.MinHits = 2
	settings.MaxMatchDistance = 100
	frameDT := 1.0 / 30.0
	started := time.Unix(1_700_000_000, 0)

	tracks, nextID := updateTracks(nil, []Detection{{
		Rect:   image.Rect(60, 60, 140, 140),
		Center: image.Pt(100, 100),
	}}, started, frameDT, 1, settings)
	tracks, nextID = updateTracks(tracks, []Detection{{
		Rect:   image.Rect(190, 60, 270, 140),
		Center: image.Pt(230, 100),
	}}, started.Add(time.Second/30), frameDT, nextID, settings)

	if len(tracks) != 1 || nextID != 2 {
		t.Fatalf("expected one continuous track, got tracks=%d nextID=%d", len(tracks), nextID)
	}
	if tracks[0].ID != 1 || tracks[0].Hits != 2 {
		t.Fatalf("fast object was split instead of matched: %+v", tracks[0])
	}
	metadata := makeFrameMetadata(2, started, started.Add(time.Second/30), tracks, settings)
	if len(metadata.Tracks) != 1 || metadata.Tracks[0].ID != 1 {
		t.Fatalf("expected the two-hit fast object in frame metadata, got %+v", metadata.Tracks)
	}
}

func TestNormalizeTrackingSettingsPreservesAcceptedZeroValues(t *testing.T) {
	settings := DefaultTrackingSettings()
	settings.MinArea = 0
	settings.SlowMinSpeed = 0
	settings.MinSpeed = 0
	settings.ForegroundThreshold = 0
	settings.PreEventDuration = 0
	settings.PostEventDuration = 0

	got := NormalizeTrackingSettings(settings)
	if got.MinArea != 0 || got.SlowMinSpeed != 0 || got.MinSpeed != 0 || got.ForegroundThreshold != 0 {
		t.Fatalf("numeric zero values were replaced: %+v", got)
	}
	if got.PreEventDuration != 0 || got.PostEventDuration != 0 {
		t.Fatalf("zero event buffers were replaced: pre=%s post=%s", got.PreEventDuration, got.PostEventDuration)
	}
}

func TestNormalizeTrackingSettingsReplacesNonFiniteValues(t *testing.T) {
	settings := DefaultTrackingSettings()
	settings.MinArea = math.NaN()
	settings.MinSpeed = math.Inf(1)
	settings.TrackingROIHeightFrac = math.NaN()
	settings.PreEventDuration = -time.Second

	got := NormalizeTrackingSettings(settings)
	defaults := DefaultTrackingSettings()
	if got.MinArea != defaults.MinArea || got.MinSpeed != defaults.MinSpeed || got.TrackingROIHeightFrac != defaults.TrackingROIHeightFrac {
		t.Fatalf("non-finite settings were not normalized: %+v", got)
	}
	if got.PreEventDuration != defaults.PreEventDuration {
		t.Fatalf("negative pre-event duration was not normalized: %s", got.PreEventDuration)
	}
}

func TestMakeTrailMetadataKeepsRecentDisplayHistoryBounded(t *testing.T) {
	points := make([]image.Point, maxMetadataTrailPoints+10)
	for i := range points {
		points[i] = image.Pt(i, i*2)
	}

	trail := makeTrailMetadata(points)
	if len(trail) != maxMetadataTrailPoints {
		t.Fatalf("unexpected trail length: got %d want %d", len(trail), maxMetadataTrailPoints)
	}
	if trail[0] != (TrailPoint{X: 10, Y: 20}) || trail[len(trail)-1] != (TrailPoint{X: len(points) - 1, Y: (len(points) - 1) * 2}) {
		t.Fatalf("trail did not retain the most recent points: first=%+v last=%+v", trail[0], trail[len(trail)-1])
	}
}

func TestBallProfileMatchesMotionBlurSizeChanges(t *testing.T) {
	settings := DefaultTrackingSettingsForProfile(trackingProfileBall)
	track := &Track{
		Rect:     image.Rect(200, 900, 260, 920),
		Position: image.Pt(230, 910),
		Hits:     8,
	}
	detection := Detection{
		Rect:   image.Rect(210, 820, 390, 940),
		Center: image.Pt(300, 880),
	}

	if _, ok := scoreTrackDetectionMatch(track, detection, 230, 910, settings); !ok {
		t.Fatal("ball profile rejected the same ball after motion blur enlarged its contour")
	}
}
