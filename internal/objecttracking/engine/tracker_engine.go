//go:build opencv

package engine

import (
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"time"

	"gocv.io/x/gocv"
)

var ErrStopTracking = errors.New("stop tracking")

const ballFrameDifferenceThreshold = 20

// TrackerConfig defines one tracker run independently of any specific UI.
type TrackerConfig struct {
	Input               string
	InputLabel          string
	OutputDir           string
	FullVideoPath       string
	ShowMask            bool
	RecordEvents        bool
	ExportOriginalVideo bool
	ExportMaskVideo     bool
	FallbackFPS         float64
	AnalysisMaxWidth    int
	PreviewMaxWidth     int
	PreviewFPS          float64
	Settings            TrackingSettings
	Nostr               NostrSettings
	Capture             CaptureMetadata
}

type NostrSettings struct {
	Enabled          bool
	RelayURL         string
	SecretKey        string
	BlossomServerURL string
	BlossomNoteURL   string
	MinTrackDistance float64
	UseObjectGIF     bool
	Timeout          time.Duration
}

// TrackerReady reports source properties once the input stream has opened.
type TrackerReady struct {
	Input          string
	InputLabel     string
	FPS            float64
	Width          int
	Height         int
	AnalysisWidth  int
	AnalysisHeight int
	TotalFrames    int
	Duration       time.Duration
	HasFixedLength bool
}

// FrameUpdate is one rendered tracker snapshot suitable for a CLI preview or a
// future GUI surface.
type FrameUpdate struct {
	SourceFrame        int
	Timestamp          time.Time
	Metadata           FrameMetadata
	DetectionCount     int
	TrackCount         int
	FastCount          int
	SlowCount          int
	Recording          bool
	LearningBackground bool
	StatusText         string
	ProgressText       string
	Elapsed            time.Duration
	TotalDuration      time.Duration
	Progress           float64
	HasFixedLength     bool
	Display            gocv.Mat
	Mask               gocv.Mat

	hasDisplay bool
	hasMask    bool
}

// HasDisplay reports whether this update contains a rendered preview.
func (u *FrameUpdate) HasDisplay() bool { return u.hasDisplay }

// Close releases any Mats owned by this update.
func (u *FrameUpdate) Close() {
	if u == nil {
		return
	}
	if u.hasDisplay {
		u.Display.Close()
		u.hasDisplay = false
	}
	if u.hasMask {
		u.Mask.Close()
		u.hasMask = false
	}
}

// TrackerHooks lets callers attach a presentation layer without changing the
// tracking pipeline itself.
type TrackerHooks struct {
	OnReady      func(TrackerReady) error
	OnFrame      func(*FrameUpdate) error
	OnEventStart func(string) error
	OnEventSaved func(string) error
}

// TrackerEngine owns one run of the capture, tracking, and event recording
// pipeline.
type TrackerEngine struct {
	Config TrackerConfig
	Hooks  TrackerHooks
	Stop   <-chan struct{}

	RawSegmentDir string
}

func (e *TrackerEngine) effectiveSettings() TrackingSettings {
	return NormalizeTrackingSettings(e.Config.Settings)
}

func (e *TrackerEngine) shouldRecordEvents() bool {
	return e.Config.RecordEvents
}

// Run executes the tracker until the input ends, the caller asks it to stop,
// or an error occurs.
func (e *TrackerEngine) Run() (runErr error) {
	if e.stopped() {
		return nil
	}

	capture, fps, err := e.openCapture()
	if err != nil {
		return err
	}
	defer capture.Close()

	if err := e.emitReady(capture, fps); err != nil {
		if errors.Is(err, ErrStopTracking) {
			return nil
		}
		return err
	}

	frame := gocv.NewMat()
	defer frame.Close()
	analysisFrame := gocv.NewMat()
	defer analysisFrame.Close()
	gray := gocv.NewMat()
	defer gray.Close()
	blurred := gocv.NewMat()
	defer blurred.Close()
	previousBlurred := gocv.NewMat()
	defer previousBlurred.Close()
	slowReference := gocv.NewMat()
	defer slowReference.Close()
	slowReferenceAge := 0
	mask := gocv.NewMat()
	defer mask.Close()
	temporalMask := gocv.NewMat()
	defer temporalMask.Close()
	slowTemporalMask := gocv.NewMat()
	defer slowTemporalMask.Close()
	darkObjectMask := gocv.NewMat()
	defer darkObjectMask.Close()
	cleanMask := gocv.NewMat()
	defer cleanMask.Close()
	settings := e.effectiveSettings()
	totalFrames := 0
	if e.Config.InputLabel == "video file" {
		totalFrames = int(math.Round(capture.Get(gocv.VideoCaptureFrameCount)))
	}

	// Dark aircraft against a bright sky must remain foreground. MOG2's shadow
	// class uses the intermediate value 127, which the configured foreground
	// threshold rejects and caused the TELE helicopter to disappear.
	detectShadows := false
	background := gocv.NewBackgroundSubtractorMOG2WithParams(settings.MOG2History, settings.MOG2VarThreshold, detectShadows)
	defer background.Close()

	openKernel := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(3, 3))
	defer openKernel.Close()
	dilateKernel := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(5, 5))
	defer dilateKernel.Close()
	darkObjectKernel := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(15, 15))
	defer darkObjectKernel.Close()
	var tracks []*Track
	var buffer []BufferedFrame
	var recorder *EventRecorder
	var segmentRecorder *RawSegmentRecorder
	var fullVideoWriter *gocv.VideoWriter
	var fullVideoMetadata *EventRecorder
	bufferDir, err := os.MkdirTemp("", "trackobject-buffer-*")
	if err != nil {
		return fmt.Errorf("create frame spool directory: %w", err)
	}
	defer os.RemoveAll(bufferDir)

	lastFrameTime := time.Time{}
	defer func() {
		runErr = errors.Join(runErr, finishTrackerRunResources(buffer, recorder, segmentRecorder, lastFrameTime))
		if fullVideoWriter != nil {
			runErr = errors.Join(runErr, fullVideoWriter.Close())
		}
		if fullVideoMetadata != nil {
			runErr = errors.Join(runErr, fullVideoMetadata.closeTrackingStream())
		}
	}()

	nextTrackID := 1
	sourceFrame := 0
	firstFrameTime := time.Time{}
	lastInteresting := time.Time{}
	liveReadFailure := time.Time{}

	for {
		if e.stopped() {
			break
		}

		if ok := capture.Read(&frame); !ok || frame.Empty() {
			if e.Config.InputLabel == "video file" {
				break
			}
			if liveReadFailure.IsZero() {
				liveReadFailure = time.Now()
			}
			if time.Since(liveReadFailure) > 5*time.Second {
				return errors.New("live stream stopped delivering frames")
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		liveReadFailure = time.Time{}

		observedAt := time.Now()
		if firstFrameTime.IsZero() {
			firstFrameTime = observedAt
		}

		sourceFrame++
		now := sourceFrameTimestamp(e.Config.InputLabel, firstFrameTime, observedAt, sourceFrame, fps)
		frameDT := sourceFrameDelta(lastFrameTime, now, fps)
		lastFrameTime = now

		if e.shouldRecordEvents() && segmentRecorder == nil && settings.RawSegmentDuration > 0 {
			segmentRecorder, err = startRawSegmentRecorder(e.Config.OutputDir, fps, frame.Cols(), frame.Rows(), settings, e.Config.Capture)
			if err != nil {
				return err
			}
			e.RawSegmentDir = segmentRecorder.Directory
		}
		if segmentRecorder != nil {
			timeOffset := time.Duration(float64(sourceFrame-1) * float64(time.Second) / fps)
			if err := segmentRecorder.RecordFrame(frame, sourceFrame, timeOffset); err != nil {
				return err
			}
		}

		analysisMaxWidth := e.Config.AnalysisMaxWidth
		if settings.Profile == trackingProfileGeneral {
			// General tracking must retain source resolution. Distant aircraft in
			// 4K TELE recordings can be only a few pixels high and disappear when
			// resized to 1080p before background subtraction.
			analysisMaxWidth = 0
		}
		processingFrame, analysisScale, err := frameForAnalysis(frame, &analysisFrame, analysisMaxWidth)
		if err != nil {
			if e.Config.FullVideoPath != "" {
				return fmt.Errorf("prepare full tracked video frame %d: %w", sourceFrame, err)
			}
			continue
		}
		analysisSettings := trackingSettingsForScale(settings, analysisScale)

		gocv.CvtColor(processingFrame, &gray, gocv.ColorBGRToGray)
		gocv.GaussianBlur(gray, &blurred, image.Pt(analysisSettings.BlurSize, analysisSettings.BlurSize), 0, 0, gocv.BorderDefault)
		hasPreviousFrame := !previousBlurred.Empty()
		if hasPreviousFrame {
			gocv.AbsDiff(blurred, previousBlurred, &temporalMask)
			gocv.Threshold(temporalMask, &temporalMask, ballFrameDifferenceThreshold, 255, gocv.ThresholdBinary)
		}
		blurred.CopyTo(&previousBlurred)
		if analysisScale < 1 {
			if slowReference.Empty() {
				blurred.CopyTo(&slowReference)
				slowReferenceAge = 0
			} else {
				gocv.AbsDiff(blurred, slowReference, &slowTemporalMask)
				gocv.Threshold(slowTemporalMask, &slowTemporalMask, ballFrameDifferenceThreshold, 255, gocv.ThresholdBinary)
				slowReferenceAge++
				if slowReferenceAge >= 15 {
					blurred.CopyTo(&slowReference)
					slowReferenceAge = 0
				}
			}
		}

		useTemporalDetections := settings.Profile == trackingProfileBall
		if settings.Profile == trackingProfileBall {
			// A rolling or thrown ball crosses the image in only a few frames. MOG2
			// can absorb it, label it as a shadow, or emit only disconnected pieces
			// of its motion blur. Consecutive-frame differencing is a better fit for
			// the stationary DWARF camera: it preserves every moving edge and does
			// not depend on the background model's learning state.
			if !hasPreviousFrame {
				continue
			}
			temporalMask.CopyTo(&cleanMask)
		} else {
			background.Apply(blurred, &mask)
			gocv.Threshold(mask, &cleanMask, float32(settings.ForegroundThreshold), 255, gocv.ThresholdBinary)
			if analysisScale < 1 && !slowTemporalMask.Empty() {
				slowTemporalMask.CopyTo(&cleanMask)
				useTemporalDetections = true
				gocv.MorphologyEx(gray, &darkObjectMask, gocv.MorphBlackhat, darkObjectKernel)
				{
					gocv.Threshold(darkObjectMask, &darkObjectMask, 14, 255, gocv.ThresholdBinary)
					motionSupport := slowTemporalMask.Clone()
					gocv.Dilate(motionSupport, &motionSupport, darkObjectKernel)
					gocv.BitwiseAnd(darkObjectMask, motionSupport, &darkObjectMask)
					gocv.BitwiseOr(cleanMask, darkObjectMask, &cleanMask)
					motionSupport.Close()
				}
			}
		}
		gocv.MorphologyEx(cleanMask, &cleanMask, gocv.MorphOpen, openKernel)
		gocv.Dilate(cleanMask, &cleanMask, dilateKernel)

		analysisROI := trackingROIForSize(processingFrame.Cols(), processingFrame.Rows(), analysisSettings)
		applyTrackingROI(&cleanMask, analysisROI)

		detectionSettings := analysisSettings
		associationSettings := settings
		if useTemporalDetections {
			// Temporal detections describe a fast object's swept motion between
			// frames. Use the tolerant association rules for that geometry even
			// when the UI has the General profile selected; classification still
			// uses the user's General speed and persistence thresholds below.
			detectionSettings.Profile = trackingProfileBall
			associationSettings.Profile = trackingProfileBall
			ballDefaults := DefaultTrackingSettingsForProfile(trackingProfileBall)
			ballMaxArea := ballDefaults.MaxArea * analysisScale * analysisScale
			if detectionSettings.MaxArea < ballMaxArea {
				detectionSettings.MaxArea = ballMaxArea
			}
			if associationSettings.MaxMatchDistance < ballDefaults.MaxMatchDistance {
				associationSettings.MaxMatchDistance = ballDefaults.MaxMatchDistance
			}
		}
		detections := findDetections(cleanMask, detectionSettings)
		if settings.Profile == trackingProfileGeneral && hasPreviousFrame && analysisScale >= 1 {
			// Recover only substantial fast transients from frame differencing.
			// Small and slow targets remain driven by MOG2, avoiding duplicate
			// detections and scene-wide noise when clouds or the camera shift.
			fastMask := temporalMask.Clone()
			gocv.MorphologyEx(fastMask, &fastMask, gocv.MorphOpen, openKernel)
			{
				gocv.Dilate(fastMask, &fastMask, dilateKernel)
				applyTrackingROI(&fastMask, trackingROIForSize(processingFrame.Cols(), processingFrame.Rows(), analysisSettings))
				fastSettings := analysisSettings
				fastSettings.Profile = trackingProfileBall
				if fastSettings.MinArea < 80 {
					fastSettings.MinArea = 80
				}
				fastDefaults := DefaultTrackingSettingsForProfile(trackingProfileBall)
				if fastSettings.MaxArea < fastDefaults.MaxArea {
					fastSettings.MaxArea = fastDefaults.MaxArea
				}
				fastDetections := findDetections(fastMask, fastSettings)
				if len(fastDetections) > 0 {
					// Fast temporal contours supplement General MOG2 detections. Replacing
					// the full set here dropped unrelated slow aircraft whenever any fast
					// motion occurred elsewhere in the frame.
					detections = append(detections, fastDetections...)
					associationSettings.Profile = trackingProfileBall
					if associationSettings.MaxMatchDistance < fastDefaults.MaxMatchDistance {
						associationSettings.MaxMatchDistance = fastDefaults.MaxMatchDistance
					}
				}
			}
			fastMask.Close()
		}
		if analysisScale < 1 {
			detections = scaleDetections(detections, 1/analysisScale)
		}
		trackingROI := trackingROIForSize(frame.Cols(), frame.Rows(), settings)
		tracks, nextTrackID = updateTracks(tracks, detections, now, frameDT, nextTrackID, associationSettings)
		tracks = filterTracksToROI(tracks, trackingROI)

		meta := makeFrameMetadata(sourceFrame, firstFrameTime, now, tracks, settings)
		meta.MeanLuma = gray.Mean().Val1
		if e.Config.FullVideoPath != "" {
			if fullVideoWriter == nil {
				fullVideoWriter, err = openEventVideoWriter(e.Config.FullVideoPath, fps, frame.Cols(), frame.Rows(), true)
				if err != nil {
					return fmt.Errorf("create full tracked video: %w", err)
				}
				fullVideoMetadata = &EventRecorder{Directory: filepath.Dir(e.Config.FullVideoPath), StartedAt: firstFrameTime, FPS: fps, Width: frame.Cols(), Height: frame.Rows(), EventID: filepath.Base(filepath.Dir(e.Config.FullVideoPath)), Settings: settings, Capture: e.Config.Capture}
				if err = fullVideoMetadata.openTrackingStream(); err != nil {
					return err
				}
			}
			if err = fullVideoMetadata.appendTrackingFrame(meta); err != nil {
				return err
			}
			if previewFrameDue(sourceFrame, fps, 3) {
				if err = fullVideoMetadata.saveTrackCropsForTracks(frame, tracks, meta); err != nil {
					return err
				}
			}
			overlay := frame.Clone()
			drawMetadataOverlay(&overlay, meta.Tracks, settings)
			err = fullVideoWriter.Write(overlay)
			overlay.Close()
			if err != nil {
				return fmt.Errorf("write full tracked video: %w", err)
			}
		}
		interesting := hasFreshInterestingTracks(tracks, settings)

		if interesting {
			lastInteresting = now
		}

		if !e.shouldRecordEvents() {
			// Full-segment processing mode still emits per-frame metadata through
			// OnFrame, but it does not open per-event video writers.
		} else if recorder == nil {
			seekableVideo := e.Config.InputLabel == "video file"
			bufferedFrame, err := bufferFrame(frame, cleanMask, bufferDir, meta, now, !seekableVideo, e.Config.ExportMaskVideo)
			if err != nil {
				return err
			}
			buffer = append(buffer, bufferedFrame)
			buffer = trimBuffer(buffer, now.Add(-settings.PreEventDuration))

			if interesting {
				sourceVideo := ""
				if seekableVideo {
					sourceVideo = e.Config.Input
				}
				recorder, err = startEvent(
					e.Config.OutputDir,
					fps,
					frame.Cols(),
					frame.Rows(),
					buffer,
					settings,
					e.Config.Capture,
					sourceVideo,
					e.Config.ExportOriginalVideo,
					e.Config.ExportMaskVideo,
				)
				if err != nil {
					return err
				}
				closeBuffer(buffer)
				buffer = nil

				if err := e.emitEventStart(recorder.Directory); err != nil {
					if errors.Is(err, ErrStopTracking) {
						break
					}
					return err
				}
			}
		} else {
			if err := recorder.RecordFrame(frame, cleanMask, tracks, meta); err != nil {
				return err
			}

			if !lastInteresting.IsZero() && now.Sub(lastInteresting) >= settings.PostEventDuration {
				dir := recorder.Directory
				finishedRecorder := recorder
				recorder = nil
				if err := finishedRecorder.Finish(now); err != nil {
					return err
				}
				lastInteresting = time.Time{}

				if err := e.emitEventSaved(dir); err != nil {
					if errors.Is(err, ErrStopTracking) {
						break
					}
					return err
				}
			}
		}

		renderPreview := e.Hooks.OnFrame != nil && previewFrameDue(sourceFrame, fps, e.Config.PreviewFPS)
		update := buildFrameUpdate(
			frame,
			cleanMask,
			tracks,
			meta,
			len(detections),
			recorder != nil,
			sourceFrame < int(fps*3),
			renderPreview,
			e.Config.ShowMask,
			e.Config.PreviewMaxWidth,
			settings,
			fps,
			totalFrames,
		)
		err = e.emitFrame(update, now)
		update.Close()
		if err != nil {
			if errors.Is(err, ErrStopTracking) {
				break
			}
			return err
		}
	}

	if recorder != nil {
		dir := recorder.Directory
		finishedRecorder := recorder
		recorder = nil
		if err := finishedRecorder.Finish(lastFrameTime); err != nil {
			return err
		}
		if err := e.emitEventSaved(dir); err != nil && !errors.Is(err, ErrStopTracking) {
			return err
		}
	}

	return nil
}

func finishTrackerRunResources(buffer []BufferedFrame, recorder *EventRecorder, segmentRecorder *RawSegmentRecorder, endedAt time.Time) error {
	closeBuffer(buffer)
	var errs []error
	if recorder != nil {
		if endedAt.IsZero() {
			endedAt = recorder.StartedAt
		}
		if err := recorder.Finish(endedAt); err != nil {
			errs = append(errs, fmt.Errorf("finalize open event recorder: %w", err))
		}
	}
	if segmentRecorder != nil {
		if err := segmentRecorder.Close(); err != nil {
			errs = append(errs, fmt.Errorf("finalize raw segment recorder: %w", err))
		}
	}
	return errors.Join(errs...)
}

func sourceFrameTimestamp(inputLabel string, anchor, observedAt time.Time, sourceFrame int, fps float64) time.Time {
	if inputLabel != "video file" || sourceFrame <= 1 || !validFPS(fps) {
		return observedAt
	}
	offset := time.Duration(float64(sourceFrame-1) * float64(time.Second) / fps)
	return anchor.Add(offset)
}

func sourceFrameDelta(previous, current time.Time, fps float64) float64 {
	if !previous.IsZero() {
		delta := current.Sub(previous).Seconds()
		if delta > 0 && delta <= 1 {
			return delta
		}
	}
	if validFPS(fps) {
		return 1.0 / fps
	}
	return 1.0 / 30.0
}

func validFPS(fps float64) bool {
	return fps > 0 && !math.IsNaN(fps) && !math.IsInf(fps, 0)
}

func (e *TrackerEngine) stopped() bool {
	if e.Stop == nil {
		return false
	}

	select {
	case <-e.Stop:
		return true
	default:
		return false
	}
}

func (e *TrackerEngine) openCapture() (*gocv.VideoCapture, float64, error) {
	capture, err := gocv.VideoCaptureFile(e.Config.Input)
	if err != nil {
		return nil, 0, fmt.Errorf("open input source: %w", err)
	}
	if !capture.IsOpened() {
		capture.Close()
		return nil, 0, errors.New("input source did not open")
	}

	fps := capture.Get(gocv.VideoCaptureFPS)
	if fps <= 1 || !validFPS(fps) {
		fps = e.Config.FallbackFPS
	}
	if !validFPS(fps) {
		fps = 30
	}

	return capture, fps, nil
}

func (e *TrackerEngine) emitReady(capture *gocv.VideoCapture, fps float64) error {
	if e.Hooks.OnReady == nil {
		return nil
	}
	totalFrames := int(math.Round(capture.Get(gocv.VideoCaptureFrameCount)))
	hasFixedLength := e.Config.InputLabel == "video file" && totalFrames > 0
	width := int(capture.Get(gocv.VideoCaptureFrameWidth))
	height := int(capture.Get(gocv.VideoCaptureFrameHeight))
	analysisWidth, analysisHeight, _ := scaledFrameSize(width, height, e.Config.AnalysisMaxWidth)
	duration := time.Duration(0)
	if hasFixedLength && fps > 0 {
		duration = time.Duration(float64(time.Second) * (float64(totalFrames) / fps))
	}
	return e.Hooks.OnReady(TrackerReady{
		Input:          e.Config.Input,
		InputLabel:     e.Config.InputLabel,
		FPS:            fps,
		Width:          width,
		Height:         height,
		AnalysisWidth:  analysisWidth,
		AnalysisHeight: analysisHeight,
		TotalFrames:    totalFrames,
		Duration:       duration,
		HasFixedLength: hasFixedLength,
	})
}

func (e *TrackerEngine) emitEventStart(dir string) error {
	if e.Hooks.OnEventStart == nil {
		return nil
	}
	return e.Hooks.OnEventStart(dir)
}

func (e *TrackerEngine) emitEventSaved(dir string) error {
	if e.Hooks.OnEventSaved == nil {
		return nil
	}
	return e.Hooks.OnEventSaved(dir)
}

func (e *TrackerEngine) emitFrame(update FrameUpdate, now time.Time) error {
	if e.Hooks.OnFrame == nil {
		return nil
	}
	update.Timestamp = now
	return e.Hooks.OnFrame(&update)
}

func buildFrameUpdate(
	frame gocv.Mat,
	cleanMask gocv.Mat,
	tracks []*Track,
	meta FrameMetadata,
	detectionCount int,
	recording bool,
	learningBackground bool,
	renderPreview bool,
	includeMask bool,
	previewMaxWidth int,
	settings TrackingSettings,
	fps float64,
	totalFrames int,
) FrameUpdate {
	fastCount, slowCount := countTrackTypes(meta.Tracks)
	status := fmt.Sprintf("FAST: %d   SLOW: %d   detections: %d   tracks: %d",
		fastCount, slowCount, detectionCount, len(tracks))

	elapsed := time.Duration(0)
	if fps > 0 && meta.SourceFrame > 0 {
		elapsed = time.Duration(float64(time.Second) * (float64(meta.SourceFrame) / fps))
	}

	totalDuration := time.Duration(0)
	hasFixedLength := fps > 0 && totalFrames > 0
	progress := 0.0
	progressText := ""
	if hasFixedLength {
		totalDuration = time.Duration(float64(time.Second) * (float64(totalFrames) / fps))
		progress = math.Min(1, float64(meta.SourceFrame)/float64(totalFrames))
		progressText = fmt.Sprintf("Video: %s / %s (%.0f%%)",
			formatVideoProgressDuration(elapsed),
			formatVideoProgressDuration(totalDuration),
			progress*100)
	}

	update := FrameUpdate{
		SourceFrame:        meta.SourceFrame,
		Metadata:           meta,
		DetectionCount:     detectionCount,
		TrackCount:         len(tracks),
		FastCount:          fastCount,
		SlowCount:          slowCount,
		Recording:          recording,
		LearningBackground: learningBackground,
		StatusText:         status,
		ProgressText:       progressText,
		Elapsed:            elapsed,
		TotalDuration:      totalDuration,
		Progress:           progress,
		HasFixedLength:     hasFixedLength,
	}

	if renderPreview {
		previewWidth, previewHeight, previewScale := scaledFrameSize(frame.Cols(), frame.Rows(), previewMaxWidth)
		if previewScale < 1 {
			update.Display = gocv.NewMat()
			gocv.Resize(frame, &update.Display, image.Pt(previewWidth, previewHeight), 0, 0, gocv.InterpolationArea)
			update.hasDisplay = true
		} else {
			update.Display = frame.Clone()
			update.hasDisplay = true
		}

		if update.hasDisplay {
			drawMetadataOverlay(&update.Display, scaleTrackMetadata(meta.Tracks, previewScale), settings)
			gocv.PutText(&update.Display, status, image.Pt(20, 30),
				gocv.FontHersheySimplex, 0.9, textColor, 2)
			if recording {
				gocv.PutText(&update.Display, "RECORDING EVENT", image.Pt(20, 60),
					gocv.FontHersheySimplex, 0.9, warnColor, 2)
			} else {
				gocv.PutText(&update.Display, fmt.Sprintf("PREBUFFER: %.0fs", settings.PreEventDuration.Seconds()),
					image.Pt(20, 60), gocv.FontHersheySimplex, 0.75, textColor, 2)
			}
			if learningBackground {
				gocv.PutText(&update.Display, "LEARNING BACKGROUND...", image.Pt(20, 90),
					gocv.FontHersheySimplex, 0.9, warnColor, 2)
			}
			if progressText != "" {
				gocv.PutText(&update.Display, progressText, image.Pt(20, 120),
					gocv.FontHersheySimplex, 0.85, textColor, 2)
			}
		}

		if includeMask {
			update.Mask = gocv.NewMat()
			gocv.Resize(cleanMask, &update.Mask, image.Pt(previewWidth, previewHeight), 0, 0, gocv.InterpolationNearestNeighbor)
			update.hasMask = true
		}
	}

	return update
}

func formatVideoProgressDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalSeconds := int(math.Round(d.Seconds()))
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
