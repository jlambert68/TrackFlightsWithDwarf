//go:build opencv

package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocv.io/x/gocv"
)

func TestSeekablePrebufferDoesNotSpoolImages(t *testing.T) {
	frame := gocv.NewMatWithSize(16, 16, gocv.MatTypeCV8UC3)
	defer frame.Close()
	mask := gocv.NewMatWithSize(16, 16, gocv.MatTypeCV8U)
	defer mask.Close()
	spoolDir := filepath.Join(t.TempDir(), "spool")
	timestamp := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)

	buffered, err := bufferFrame(frame, mask, spoolDir, FrameMetadata{SourceFrame: 7}, timestamp, false, false)
	if err != nil {
		t.Fatalf("buffer source reference: %v", err)
	}
	if buffered.ImagePath != "" || buffered.MaskPath != "" || buffered.Metadata.SourceFrame != 7 {
		t.Fatalf("unexpected source-backed frame: %+v", buffered)
	}
	if _, err := os.Stat(spoolDir); !os.IsNotExist(err) {
		t.Fatalf("seekable prebuffer wrote to disk, stat error=%v", err)
	}
}

func TestFlushBufferedFramesReadsRequestedSourceFrames(t *testing.T) {
	root := t.TempDir()
	videoPath := writeRecorderTestVideo(t, root)

	startedAt := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	recorder := &EventRecorder{
		Directory: root,
		StartedAt: startedAt,
		EventID:   "event",
		FPS:       10,
		Width:     32,
		Height:    32,
		SeenIDs:   make(map[int]struct{}),
		Settings:  DefaultTrackingSettings(),
		MinLuma:   255,
	}
	if err := recorder.openTrackingStream(); err != nil {
		t.Fatalf("open tracking stream: %v", err)
	}
	buffers := recorderTestBuffers(startedAt)
	if err := recorder.flushBufferedFrames(buffers, videoPath); err != nil {
		t.Fatalf("flush source-backed frames: %v", err)
	}
	if err := recorder.closeTrackingStream(); err != nil {
		t.Fatalf("close tracking stream: %v", err)
	}
	if recorder.FramesWritten != 2 || recorder.TrackingFrameCount != 2 {
		t.Fatalf("unexpected flushed frame counts: frames=%d metadata=%d", recorder.FramesWritten, recorder.TrackingFrameCount)
	}
}

func TestStartEventWritesTrackedVideoOnlyByDefault(t *testing.T) {
	root := t.TempDir()
	videoPath := writeRecorderTestVideo(t, root)
	startedAt := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	recorder, err := startEvent(
		filepath.Join(root, "events"),
		10,
		32,
		32,
		recorderTestBuffers(startedAt),
		DefaultTrackingSettings(),
		CaptureMetadata{},
		videoPath,
		false,
		false,
	)
	if err != nil {
		t.Fatalf("start tracked-only event: %v", err)
	}
	if err := recorder.Finish(startedAt.Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("finish tracked-only event: %v", err)
	}

	if _, err := os.Stat(filepath.Join(recorder.Directory, "tracked.avi")); err != nil {
		t.Fatalf("tracked video was not written: %v", err)
	}
	for _, filename := range []string{"original.avi", "masked.avi"} {
		if _, err := os.Stat(filepath.Join(recorder.Directory, filename)); !os.IsNotExist(err) {
			t.Fatalf("optional output %s was written, stat error=%v", filename, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(recorder.Directory, "event.json"))
	if err != nil {
		t.Fatalf("read event summary: %v", err)
	}
	var summary EventSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("decode event summary: %v", err)
	}
	if summary.TrackedVideo != "tracked.avi" || summary.OriginalVideo != "" || summary.MaskedVideo != "" {
		t.Fatalf("unexpected event outputs: %+v", summary)
	}
}

func TestMaskedWriterAcceptsResizedGrayscaleFrames(t *testing.T) {
	root := t.TempDir()
	maskPath := filepath.Join(root, "masked.avi")
	maskWriter, err := openEventVideoWriter(maskPath, 10, 32, 32, false)
	if err != nil {
		t.Fatalf("create grayscale mask writer: %v", err)
	}
	startedAt := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	recorder := &EventRecorder{
		MaskedWriter: maskWriter,
		WriteMask:    true,
		Directory:    root,
		StartedAt:    startedAt,
		EventID:      "event",
		FPS:          10,
		Width:        32,
		Height:       32,
		SeenIDs:      make(map[int]struct{}),
		Settings:     DefaultTrackingSettings(),
		MinLuma:      255,
	}
	if err := recorder.openTrackingStream(); err != nil {
		maskWriter.Close()
		t.Fatalf("open tracking stream: %v", err)
	}
	frame := gocv.NewMatWithSize(32, 32, gocv.MatTypeCV8UC3)
	defer frame.Close()
	mask := gocv.NewMatWithSizeFromScalar(gocv.NewScalar(255, 0, 0, 0), 16, 16, gocv.MatTypeCV8U)
	defer mask.Close()
	if err := recorder.RecordFrame(frame, mask, nil, FrameMetadata{SourceFrame: 1, TimeUnixNS: startedAt.UnixNano()}); err != nil {
		t.Fatalf("record grayscale mask: %v", err)
	}
	if err := recorder.Finish(startedAt.Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("finish grayscale mask recording: %v", err)
	}

	capture, err := gocv.VideoCaptureFile(maskPath)
	if err != nil {
		t.Fatalf("open grayscale mask recording: %v", err)
	}
	defer capture.Close()
	decoded := gocv.NewMat()
	defer decoded.Close()
	if ok := capture.Read(&decoded); !ok || decoded.Empty() || decoded.Cols() != 32 || decoded.Rows() != 32 {
		t.Fatalf("invalid decoded grayscale mask: ok=%v size=%dx%d", ok, decoded.Cols(), decoded.Rows())
	}
}

func writeRecorderTestVideo(t *testing.T, root string) string {
	t.Helper()
	videoPath := filepath.Join(root, "source.avi")
	writer, err := openEventVideoWriter(videoPath, 10, 32, 32, true)
	if err != nil {
		t.Fatalf("create source video: %v", err)
	}
	for i := 0; i < 4; i++ {
		frame := gocv.NewMatWithSizeFromScalar(gocv.NewScalar(float64(i*30), 0, 0, 0), 32, 32, gocv.MatTypeCV8UC3)
		if err := writer.Write(frame); err != nil {
			frame.Close()
			writer.Close()
			t.Fatalf("write source frame: %v", err)
		}
		frame.Close()
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close source video: %v", err)
	}
	return videoPath
}

func recorderTestBuffers(startedAt time.Time) []BufferedFrame {
	return []BufferedFrame{
		{Timestamp: startedAt, Metadata: FrameMetadata{SourceFrame: 2, TimeUnixNS: startedAt.UnixNano(), MeanLuma: 10}},
		{Timestamp: startedAt.Add(100 * time.Millisecond), Metadata: FrameMetadata{SourceFrame: 3, TimeUnixNS: startedAt.Add(100 * time.Millisecond).UnixNano(), MeanLuma: 20}},
	}
}
