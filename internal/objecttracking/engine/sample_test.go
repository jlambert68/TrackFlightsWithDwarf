//go:build opencv

package engine

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSampleVideoProducesTrackingUpdates(t *testing.T) {
	input := filepath.Join("..", "..", "..", "testdata", "tracking", "tele-sample.mp4")
	frames, previews := 0, 0
	tracker := TrackerEngine{Config: TrackerConfig{Input: input, InputLabel: "video file", OutputDir: t.TempDir(), FallbackFPS: 30, PreviewFPS: 10, PreviewMaxWidth: 960, Settings: DefaultTrackingSettings()}, Hooks: TrackerHooks{OnFrame: func(update *FrameUpdate) error {
		frames++
		if update.HasDisplay() {
			previews++
		}
		if frames >= 10 {
			return ErrStopTracking
		}
		return nil
	}}}
	if err := tracker.Run(); err != nil && !errors.Is(err, ErrStopTracking) {
		t.Fatal(err)
	}
	if frames < 10 || previews == 0 {
		t.Fatalf("got %d frames and %d previews", frames, previews)
	}
}
