//go:build opencv

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gocv.io/x/gocv"
)

type segmentBufferFrame struct {
	SourceFrame int
	TimeOffset  time.Duration
	Frame       gocv.Mat
}

type RawSegmentRecorder struct {
	Directory string
	SessionID string
	Manifest  RawSegmentManifest

	segmentFrames int
	overlapFrames int

	currentWriter      *gocv.VideoWriter
	currentSegment     RawVideoSegment
	currentFrames      int
	lastSourceFrame    int
	lastSourceTimeMS   int64
	currentSegmentPath string

	overlapBuffer []segmentBufferFrame
}

func startRawSegmentRecorder(outputRoot string, fps float64, width, height int, settings TrackingSettings, capture CaptureMetadata) (*RawSegmentRecorder, error) {
	if settings.RawSegmentDuration <= 0 {
		return nil, nil
	}

	segmentFrames := durationToFrameCount(settings.RawSegmentDuration, fps)
	overlapFrames := durationToFrameCount(settings.RawSegmentOverlap, fps)
	if segmentFrames < 2 {
		return nil, fmt.Errorf("raw segment duration %s is too short for %.3f FPS", settings.RawSegmentDuration, fps)
	}
	if overlapFrames >= segmentFrames {
		return nil, fmt.Errorf("raw segment overlap %s must be shorter than raw segment duration %s", settings.RawSegmentOverlap, settings.RawSegmentDuration)
	}

	sessionID := time.Now().Format("2006-01-02_150405.000")
	dir := filepath.Join(outputRoot, "raw_segments", sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create raw segment directory: %w", err)
	}

	return &RawSegmentRecorder{
		Directory: dir,
		SessionID: sessionID,
		Manifest: RawSegmentManifest{
			SessionID:       sessionID,
			CreatedAt:       time.Now(),
			FPS:             fps,
			Width:           width,
			Height:          height,
			Capture:         capture,
			SegmentDuration: settings.RawSegmentDuration,
			SegmentOverlap:  settings.RawSegmentOverlap,
		},
		segmentFrames: segmentFrames,
		overlapFrames: overlapFrames,
	}, nil
}

func (r *RawSegmentRecorder) RecordFrame(frame gocv.Mat, sourceFrame int, timeOffset time.Duration) error {
	if r == nil {
		return nil
	}

	if r.currentWriter == nil {
		if err := r.openSegment(r.overlapBuffer); err != nil {
			return err
		}
	}

	if r.currentFrames == 0 {
		r.currentSegment.StartFrame = sourceFrame
		r.currentSegment.StartTimeMS = timeOffset.Milliseconds()
	}

	if err := r.currentWriter.Write(frame); err != nil {
		return fmt.Errorf("write raw segment %s: %w", r.currentSegmentPath, err)
	}

	r.currentFrames++
	r.lastSourceFrame = sourceFrame
	r.lastSourceTimeMS = timeOffset.Milliseconds()
	r.pushOverlapFrame(frame, sourceFrame, timeOffset)

	if r.currentFrames >= r.segmentFrames {
		if err := r.finishCurrentSegment(0); err != nil {
			return err
		}
	}

	return nil
}

func (r *RawSegmentRecorder) Close() error {
	if r == nil {
		return nil
	}

	var errs []error
	if r.currentWriter != nil {
		if err := r.finishCurrentSegment(0); err != nil {
			errs = append(errs, err)
		}
	}
	if err := r.writeManifest(); err != nil {
		errs = append(errs, err)
	}
	r.closeOverlapBuffer()
	return errors.Join(errs...)
}

func (r *RawSegmentRecorder) openSegment(prefill []segmentBufferFrame) error {
	fileName := fmt.Sprintf("segment_%06d.avi", len(r.Manifest.Segments)+1)
	path := filepath.Join(r.Directory, fileName)

	writer, err := gocv.VideoWriterFile(path, "MJPG", r.Manifest.FPS, r.Manifest.Width, r.Manifest.Height, true)
	if err != nil {
		return fmt.Errorf("create raw segment writer: %w", err)
	}
	if !writer.IsOpened() {
		writer.Close()
		return fmt.Errorf("raw segment writer did not open: %s", path)
	}

	r.currentWriter = writer
	r.currentSegmentPath = path
	r.currentFrames = 0
	r.currentSegment = RawVideoSegment{
		Index:               len(r.Manifest.Segments) + 1,
		File:                fileName,
		OverlapBeforeFrames: len(prefill),
	}

	if len(prefill) > 0 {
		r.currentSegment.StartFrame = prefill[0].SourceFrame
		r.currentSegment.StartTimeMS = prefill[0].TimeOffset.Milliseconds()
	}

	for _, buffered := range prefill {
		if err := r.currentWriter.Write(buffered.Frame); err != nil {
			r.currentWriter.Close()
			r.currentWriter = nil
			return fmt.Errorf("prefill raw segment %s: %w", path, err)
		}
		r.currentFrames++
		r.lastSourceFrame = buffered.SourceFrame
		r.lastSourceTimeMS = buffered.TimeOffset.Milliseconds()
	}
	if len(prefill) > 0 && len(r.Manifest.Segments) > 0 {
		r.Manifest.Segments[len(r.Manifest.Segments)-1].OverlapAfterFrames = len(prefill)
	}

	return nil
}

func (r *RawSegmentRecorder) finishCurrentSegment(overlapAfterFrames int) error {
	if r.currentWriter == nil {
		return nil
	}

	closeErr := r.currentWriter.Close()
	r.currentWriter = nil
	r.currentSegment.EndFrame = r.lastSourceFrame
	r.currentSegment.EndTimeMS = r.lastSourceTimeMS
	r.currentSegment.Frames = r.currentFrames
	r.currentSegment.OverlapAfterFrames = overlapAfterFrames
	r.Manifest.Segments = append(r.Manifest.Segments, r.currentSegment)
	r.currentSegment = RawVideoSegment{}
	r.currentFrames = 0
	r.currentSegmentPath = ""
	if closeErr != nil {
		return fmt.Errorf("close raw segment writer: %w", closeErr)
	}
	return nil
}

func (r *RawSegmentRecorder) pushOverlapFrame(frame gocv.Mat, sourceFrame int, timeOffset time.Duration) {
	if r.overlapFrames <= 0 {
		return
	}

	r.overlapBuffer = append(r.overlapBuffer, segmentBufferFrame{
		SourceFrame: sourceFrame,
		TimeOffset:  timeOffset,
		Frame:       frame.Clone(),
	})

	for len(r.overlapBuffer) > r.overlapFrames {
		r.overlapBuffer[0].Frame.Close()
		r.overlapBuffer = r.overlapBuffer[1:]
	}
}

func (r *RawSegmentRecorder) closeOverlapBuffer() {
	for i := range r.overlapBuffer {
		r.overlapBuffer[i].Frame.Close()
	}
	r.overlapBuffer = nil
}

func (r *RawSegmentRecorder) writeManifest() error {
	data, err := json.MarshalIndent(r.Manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal raw segment manifest: %w", err)
	}
	path := filepath.Join(r.Directory, "manifest.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write raw segment manifest: %w", err)
	}
	return nil
}

func durationToFrameCount(duration time.Duration, fps float64) int {
	if duration <= 0 || fps <= 0 {
		return 0
	}
	frames := int(duration.Seconds() * fps)
	if frames < 1 {
		return 1
	}
	return frames
}
