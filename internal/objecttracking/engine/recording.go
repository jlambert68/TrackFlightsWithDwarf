//go:build opencv

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jlambert/trackflights/internal/objecttracking/applog"

	"gocv.io/x/gocv"
)

const (
	minTrackCropWidth  = 96
	minTrackCropHeight = 96
)

// trimBuffer keeps only the pre-event time window and removes temporary files
// for frames that have aged out.
func trimBuffer(buffer []BufferedFrame, cutoff time.Time) []BufferedFrame {
	firstKeep := 0
	for firstKeep < len(buffer) && buffer[firstKeep].Timestamp.Before(cutoff) {
		removeBufferedFrameFiles(buffer[firstKeep])
		firstKeep++
	}

	if firstKeep == 0 {
		return buffer
	}
	return buffer[firstKeep:]
}

// closeBuffer removes temporary files still owned by the pre-event buffer.
func closeBuffer(buffer []BufferedFrame) {
	for i := range buffer {
		removeBufferedFrameFiles(buffer[i])
	}
}

func removeBufferedFrameFiles(frame BufferedFrame) {
	if frame.ImagePath != "" {
		_ = os.Remove(frame.ImagePath)
	}
	if frame.MaskPath != "" {
		_ = os.Remove(frame.MaskPath)
	}
}

func bufferFrame(frame gocv.Mat, mask gocv.Mat, dir string, meta FrameMetadata, timestamp time.Time, includeImage, includeMask bool) (BufferedFrame, error) {
	if !includeImage && !includeMask {
		return BufferedFrame{Timestamp: timestamp, Metadata: meta}, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return BufferedFrame{}, fmt.Errorf("create frame spool directory: %w", err)
	}

	base := fmt.Sprintf("frame_%06d_%019d", meta.SourceFrame, timestamp.UnixNano())
	imagePath := ""
	if includeImage {
		imagePath = filepath.Join(dir, base+".jpg")
		if ok := gocv.IMWrite(imagePath, frame); !ok {
			return BufferedFrame{}, fmt.Errorf("spool buffered frame %s", imagePath)
		}
	}

	maskPath := ""
	if includeMask {
		maskPath = filepath.Join(dir, base+".png")
		if ok := gocv.IMWrite(maskPath, mask); !ok {
			if imagePath != "" {
				_ = os.Remove(imagePath)
			}
			return BufferedFrame{}, fmt.Errorf("spool buffered mask %s", maskPath)
		}
	}

	return BufferedFrame{
		ImagePath: imagePath,
		MaskPath:  maskPath,
		Timestamp: timestamp,
		Metadata:  meta,
	}, nil
}

// startEvent creates a new event directory, opens the configured video writers,
// initializes metadata, and flushes the pre-event buffer into the recording.
func startEvent(
	outputRoot string,
	fps float64,
	width, height int,
	buffer []BufferedFrame,
	settings TrackingSettings,
	capture CaptureMetadata,
	sourceVideo string,
	writeOriginal bool,
	writeMask bool,
) (*EventRecorder, error) {
	if len(buffer) == 0 {
		return nil, errors.New("cannot start event with empty buffer")
	}

	startedAt := buffer[0].Timestamp
	eventID := startedAt.Format("2006-01-02_150405.000")
	dir := filepath.Join(outputRoot, eventID)

	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(outputRoot, fmt.Sprintf("%s_%02d", eventID, suffix))
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	var (
		rawWriter *gocv.VideoWriter
		err       error
	)
	if writeOriginal {
		rawWriter, err = openEventVideoWriter(filepath.Join(dir, "original.avi"), fps, width, height, true)
		if err != nil {
			return nil, fmt.Errorf("create original video: %w", err)
		}
	}

	trackedWriter, err := openEventVideoWriter(filepath.Join(dir, "tracked.avi"), fps, width, height, true)
	if err != nil {
		if rawWriter != nil {
			rawWriter.Close()
		}
		return nil, fmt.Errorf("create tracked video: %w", err)
	}

	var maskedWriter *gocv.VideoWriter
	if writeMask {
		maskedWriter, err = openEventVideoWriter(filepath.Join(dir, "masked.avi"), fps, width, height, false)
		if err != nil {
			if rawWriter != nil {
				rawWriter.Close()
			}
			trackedWriter.Close()
			return nil, fmt.Errorf("create masked video: %w", err)
		}
	}

	recorder := &EventRecorder{
		RawWriter:     rawWriter,
		TrackedWriter: trackedWriter,
		MaskedWriter:  maskedWriter,
		WriteOriginal: writeOriginal,
		WriteMask:     writeMask,
		Directory:     dir,
		StartedAt:     startedAt,
		FPS:           fps,
		Width:         width,
		Height:        height,
		EventID:       filepath.Base(dir),
		SeenIDs:       make(map[int]struct{}),
		Settings:      NormalizeTrackingSettings(settings),
		Capture:       capture,
		MinLuma:       255,
	}

	if err := recorder.openTrackingStream(); err != nil {
		_ = recorder.closeWriters()
		return nil, err
	}

	if err := recorder.flushBufferedFrames(buffer, sourceVideo); err != nil {
		_ = recorder.closeWriters()
		_ = recorder.closeTrackingStream()
		return nil, err
	}

	return recorder, nil
}

func (r *EventRecorder) flushBufferedFrames(buffer []BufferedFrame, sourceVideo string) error {
	var sourceCapture *gocv.VideoCapture
	nextSourceFrame := 0
	defer func() {
		if sourceCapture != nil {
			sourceCapture.Close()
		}
	}()

	for _, buffered := range buffer {
		frame := gocv.NewMat()
		if buffered.ImagePath != "" {
			frame.Close()
			frame = gocv.IMRead(buffered.ImagePath, gocv.IMReadColor)
			if frame.Empty() {
				frame.Close()
				return fmt.Errorf("read buffered frame %s", buffered.ImagePath)
			}
		} else {
			if sourceVideo == "" || buffered.Metadata.SourceFrame < 1 {
				frame.Close()
				return errors.New("buffered video frame has no seekable source")
			}
			if sourceCapture == nil {
				var err error
				sourceCapture, err = gocv.VideoCaptureFile(sourceVideo)
				if err != nil {
					frame.Close()
					return fmt.Errorf("open pre-event source video: %w", err)
				}
				if !sourceCapture.IsOpened() {
					frame.Close()
					return errors.New("pre-event source video did not open")
				}
			}
			if buffered.Metadata.SourceFrame != nextSourceFrame {
				sourceCapture.Set(gocv.VideoCapturePosFrames, float64(buffered.Metadata.SourceFrame-1))
			}
			if ok := sourceCapture.Read(&frame); !ok || frame.Empty() {
				frame.Close()
				return fmt.Errorf("decode buffered source frame %d", buffered.Metadata.SourceFrame)
			}
			nextSourceFrame = buffered.Metadata.SourceFrame + 1
		}

		mask := gocv.NewMat()
		if r.MaskedWriter != nil {
			mask.Close()
			mask = gocv.IMRead(buffered.MaskPath, gocv.IMReadGrayScale)
			if mask.Empty() {
				frame.Close()
				mask.Close()
				return fmt.Errorf("read buffered mask %s", buffered.MaskPath)
			}
		}

		if err := r.RecordFrame(frame, mask, nil, buffered.Metadata); err != nil {
			frame.Close()
			mask.Close()
			return err
		}
		frame.Close()
		mask.Close()
	}
	return nil
}

func openEventVideoWriter(path string, fps float64, width, height int, isColor bool) (*gocv.VideoWriter, error) {
	writer, err := gocv.VideoWriterFile(path, "MJPG", fps, width, height, isColor)
	if err != nil {
		return nil, err
	}
	if !writer.IsOpened() {
		writer.Close()
		return nil, errors.New("video writer did not open")
	}
	return writer, nil
}

// RecordFrame writes one raw frame, creates the annotated tracked frame, and
// appends the JSON metadata for that frame.
func (r *EventRecorder) RecordFrame(clean gocv.Mat, mask gocv.Mat, tracks []*Track, meta FrameMetadata) error {
	meta.TimeMS = time.Unix(0, meta.TimeUnixNS).Sub(r.StartedAt).Milliseconds()

	if r.RawWriter != nil {
		if err := r.RawWriter.Write(clean); err != nil {
			return fmt.Errorf("write original video: %w", err)
		}
	}

	if r.TrackedWriter != nil {
		overlay := clean.Clone()
		drawMetadataOverlay(&overlay, meta.Tracks, r.Settings)
		if err := r.TrackedWriter.Write(overlay); err != nil {
			overlay.Close()
			return fmt.Errorf("write tracked video: %w", err)
		}
		overlay.Close()
	}

	if r.MaskedWriter != nil {
		if mask.Empty() {
			return errors.New("write masked video: empty mask frame")
		}
		maskFrame := mask
		resizedMask := gocv.NewMat()
		defer resizedMask.Close()
		if mask.Cols() != r.Width || mask.Rows() != r.Height {
			gocv.Resize(mask, &resizedMask, image.Pt(r.Width, r.Height), 0, 0, gocv.InterpolationNearestNeighbor)
			maskFrame = resizedMask
		}
		if err := r.MaskedWriter.Write(maskFrame); err != nil {
			return fmt.Errorf("write masked video: %w", err)
		}
	}

	if len(tracks) > 0 {
		if err := r.saveTrackCropsForTracks(clean, tracks, meta); err != nil {
			return err
		}
	} else if err := r.saveTrackCrops(clean, meta); err != nil {
		return err
	}

	for _, t := range meta.Tracks {
		r.SeenIDs[t.ID] = struct{}{}
		if t.Speed > r.HighestSpeed {
			r.HighestSpeed = t.Speed
		}
	}
	r.LumaSum += meta.MeanLuma
	if meta.MeanLuma < r.MinLuma {
		r.MinLuma = meta.MeanLuma
	}
	if meta.MeanLuma > r.MaxLuma {
		r.MaxLuma = meta.MeanLuma
	}

	if err := r.appendTrackingFrame(meta); err != nil {
		return err
	}

	r.FramesWritten++
	return nil
}

// Finish closes the writers and emits the final JSON files for the event.
func (r *EventRecorder) Finish(endedAt time.Time) error {
	var errs []error

	if err := r.closeWriters(); err != nil {
		errs = append(errs, err)
	}
	if err := r.closeTrackingStream(); err != nil {
		errs = append(errs, err)
	}

	photometry := PhotometricSummary{MinLuma: r.MinLuma, MaxLuma: r.MaxLuma, Samples: r.FramesWritten}
	if r.FramesWritten > 0 {
		photometry.MeanLuma = r.LumaSum / float64(r.FramesWritten)
	} else {
		photometry.MinLuma = 0
	}

	originalVideo := ""
	if r.WriteOriginal {
		originalVideo = "original.avi"
	}
	maskedVideo := ""
	if r.WriteMask {
		maskedVideo = "masked.avi"
	}

	summary := EventSummary{
		EventID:           r.EventID,
		StartedAt:         r.StartedAt,
		EndedAt:           endedAt,
		DurationSeconds:   endedAt.Sub(r.StartedAt).Seconds(),
		Width:             r.Width,
		Height:            r.Height,
		FPS:               r.FPS,
		Frames:            r.FramesWritten,
		UniqueObjects:     len(r.SeenIDs),
		HighestSpeedPxSec: r.HighestSpeed,
		OriginalVideo:     originalVideo,
		TrackedVideo:      "tracked.avi",
		MaskedVideo:       maskedVideo,
		TrackCropsDir:     "track_crops",
		TrackNamesFile:    "track_names.json",
		TrackingMetadata:  "tracking.json",
		TrackingSettings:  r.Settings,
		Capture:           r.Capture,
		Photometry:        photometry,
	}

	summaryData, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		errs = append(errs, err)
	} else if err := os.WriteFile(filepath.Join(r.Directory, "event.json"), summaryData, 0644); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func ensureObjectGIF(objectDir string) (string, error) {
	outputPath := filepath.Join(objectDir, "object.gif")
	if _, err := os.Stat(outputPath); err == nil {
		return outputPath, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat object gif %s: %w", outputPath, err)
	}

	cropPaths, err := listTrackCropPathsForGIF(objectDir)
	if err != nil {
		return "", err
	}
	if len(cropPaths) == 0 {
		return "", nil
	}

	applog.InfofID("60f93aae-52df-4341-aa72-b8b7fd31540d", "gif generation writing object_dir=%s output=%s frame_count=%d", objectDir, outputPath, len(cropPaths))
	if err := writeObjectGIF(outputPath, cropPaths); err != nil {
		return "", err
	}
	return outputPath, nil
}

func (r *EventRecorder) generateObjectGIFs() error {
	root := filepath.Join(r.Directory, "track_crops")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read track crops directory: %w", err)
	}

	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		objectDir := filepath.Join(root, entry.Name())
		applog.InfofID("7fe1e4bd-90fc-4767-aef9-540c6fb7c6ae", "gif generation event=%s object_dir=%s", r.EventID, objectDir)
		cropPaths, err := listTrackCropPathsForGIF(objectDir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(cropPaths) == 0 {
			applog.InfofID("160da204-f40d-4894-a689-c9a325d37375", "gif generation skipped event=%s object_dir=%s reason=no-crop-frames", r.EventID, objectDir)
			continue
		}
		outputPath, err := ensureObjectGIF(objectDir)
		if err != nil {
			applog.ErrorfID("4dcc36b7-b026-4c17-9ad5-bdc71e0a75d1", "gif generation failed event=%s object_dir=%s output=%s error=%v", r.EventID, objectDir, outputPath, err)
			errs = append(errs, err)
			continue
		}
		if outputPath == "" {
			applog.InfofID("728fcf9a-5989-4ac4-a306-2a3ef2030f96", "gif generation skipped event=%s object_dir=%s reason=no-crop-frames", r.EventID, objectDir)
			continue
		}
		applog.InfofID("1d878f7d-bf13-40ba-baf9-31f80285d739", "gif generation complete event=%s object_dir=%s output=%s frame_count=%d", r.EventID, objectDir, outputPath, len(cropPaths))
	}
	return errors.Join(errs...)
}

func listTrackCropPathsForGIF(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read track crop directory %s: %w", dir, err)
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasSuffix(name, ".jpg") && !strings.HasSuffix(name, ".jpeg") && !strings.HasSuffix(name, ".png") {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func writeObjectGIF(outputPath string, framePaths []string) error {
	frames := make([]image.Image, 0, len(framePaths))
	maxWidth := 0
	maxHeight := 0
	for _, framePath := range framePaths {
		file, err := os.Open(framePath)
		if err != nil {
			return fmt.Errorf("open crop frame %s: %w", framePath, err)
		}
		img, _, err := image.Decode(file)
		_ = file.Close()
		if err != nil {
			return fmt.Errorf("decode crop frame %s: %w", framePath, err)
		}
		frames = append(frames, img)
		if width := img.Bounds().Dx(); width > maxWidth {
			maxWidth = width
		}
		if height := img.Bounds().Dy(); height > maxHeight {
			maxHeight = height
		}
	}
	if len(frames) == 0 || maxWidth <= 0 || maxHeight <= 0 {
		return nil
	}

	animation := &gif.GIF{
		Image: make([]*image.Paletted, 0, len(frames)),
		Delay: make([]int, 0, len(frames)),
	}
	canvasBounds := image.Rect(0, 0, maxWidth, maxHeight)
	for _, frame := range frames {
		rgba := image.NewRGBA(canvasBounds)
		offset := image.Pt(
			(maxWidth-frame.Bounds().Dx())/2,
			(maxHeight-frame.Bounds().Dy())/2,
		)
		targetRect := image.Rectangle{Min: offset, Max: offset.Add(frame.Bounds().Size())}
		draw.Draw(rgba, targetRect, frame, frame.Bounds().Min, draw.Src)

		paletted := image.NewPaletted(canvasBounds, palette.Plan9)
		draw.FloydSteinberg.Draw(paletted, canvasBounds, rgba, image.Point{})
		animation.Image = append(animation.Image, paletted)
		animation.Delay = append(animation.Delay, 6)
	}
	animation.LoopCount = 0

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create object gif %s: %w", outputPath, err)
	}
	defer file.Close()
	if err := gif.EncodeAll(file, animation); err != nil {
		return fmt.Errorf("encode object gif %s: %w", outputPath, err)
	}
	return nil
}

func (r *EventRecorder) saveTrackCrops(frame gocv.Mat, meta FrameMetadata) error {
	if frame.Empty() || len(meta.Tracks) == 0 {
		return nil
	}

	root := filepath.Join(r.Directory, "track_crops")
	frameBounds := image.Rect(0, 0, frame.Cols(), frame.Rows())

	for _, track := range meta.Tracks {
		rect := image.Rect(
			track.BoxX,
			track.BoxY,
			track.BoxX+track.BoxWidth,
			track.BoxY+track.BoxHeight,
		)
		rect = expandedTrackCropRect(rect).Intersect(frameBounds)
		if rect.Empty() {
			continue
		}

		objectDir := filepath.Join(root, fmt.Sprintf("object_%04d", track.ID))
		if err := os.MkdirAll(objectDir, 0755); err != nil {
			return fmt.Errorf("create track crop directory: %w", err)
		}

		crop := frame.Region(rect)
		filename := filepath.Join(objectDir,
			fmt.Sprintf("frame_%06d_%09dms.jpg", meta.SourceFrame, meta.TimeMS))
		if ok := gocv.IMWrite(filename, crop); !ok {
			crop.Close()
			return fmt.Errorf("save track crop %s", filename)
		}
		crop.Close()
	}

	return nil
}

func (r *EventRecorder) saveTrackCropsForTracks(frame gocv.Mat, tracks []*Track, meta FrameMetadata) error {
	if frame.Empty() || len(tracks) == 0 {
		return nil
	}

	root := filepath.Join(r.Directory, "track_crops")
	frameBounds := image.Rect(0, 0, frame.Cols(), frame.Rows())

	for _, track := range tracks {
		if track == nil || track.Hits < r.Settings.MinHits || track.Missed > 0 || track.Rect.Empty() {
			continue
		}

		rect := expandedTrackCropRect(track.Rect).Intersect(frameBounds)
		if rect.Empty() {
			continue
		}

		objectDir := filepath.Join(root, fmt.Sprintf("object_%04d", track.ID))
		if err := os.MkdirAll(objectDir, 0755); err != nil {
			return fmt.Errorf("create track crop directory: %w", err)
		}

		crop := frame.Region(rect)
		filename := filepath.Join(objectDir,
			fmt.Sprintf("frame_%06d_%09dms.jpg", meta.SourceFrame, meta.TimeMS))
		if ok := gocv.IMWrite(filename, crop); !ok {
			crop.Close()
			return fmt.Errorf("save track crop %s", filename)
		}
		crop.Close()
	}

	return nil
}

func expandedTrackCropRect(rect image.Rectangle) image.Rectangle {
	rect = scaleRectAroundCenter(rect, trackBoxScale)
	if rect.Empty() {
		return rect
	}

	centerX := (rect.Min.X + rect.Max.X) / 2
	centerY := (rect.Min.Y + rect.Max.Y) / 2
	width := rect.Dx()
	height := rect.Dy()
	if width < minTrackCropWidth {
		width = minTrackCropWidth
	}
	if height < minTrackCropHeight {
		height = minTrackCropHeight
	}

	halfWidth := width / 2
	halfHeight := height / 2
	return image.Rect(
		centerX-halfWidth,
		centerY-halfHeight,
		centerX-halfWidth+width,
		centerY-halfHeight+height,
	)
}

func (r *EventRecorder) openTrackingStream() error {
	trackingPath := filepath.Join(r.Directory, "tracking.json")
	file, err := os.Create(trackingPath)
	if err != nil {
		return fmt.Errorf("create tracking metadata: %w", err)
	}

	header := struct {
		EventID   string          `json:"event_id"`
		StartedAt time.Time       `json:"started_at"`
		FPS       float64         `json:"fps"`
		Width     int             `json:"width"`
		Height    int             `json:"height"`
		Capture   CaptureMetadata `json:"capture"`
	}{
		EventID:   r.EventID,
		StartedAt: r.StartedAt,
		FPS:       r.FPS,
		Width:     r.Width,
		Height:    r.Height,
		Capture:   r.Capture,
	}

	data, err := json.Marshal(header)
	if err != nil {
		file.Close()
		return fmt.Errorf("marshal tracking header: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != '}' {
		file.Close()
		return errors.New("tracking header JSON was malformed")
	}

	if _, err := file.Write(data[:len(data)-1]); err != nil {
		file.Close()
		return fmt.Errorf("write tracking header: %w", err)
	}
	if _, err := file.WriteString(",\"frames\":["); err != nil {
		file.Close()
		return fmt.Errorf("start tracking frame stream: %w", err)
	}

	r.TrackingFile = file
	r.TrackingStreamOpen = true
	r.TrackingFrameCount = 0
	return nil
}

func (r *EventRecorder) appendTrackingFrame(meta FrameMetadata) error {
	if r.TrackingFile == nil || !r.TrackingStreamOpen {
		return errors.New("tracking stream is not open")
	}

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal frame metadata: %w", err)
	}

	if r.TrackingFrameCount > 0 {
		if _, err := r.TrackingFile.WriteString(","); err != nil {
			return fmt.Errorf("separate frame metadata: %w", err)
		}
	}
	if _, err := r.TrackingFile.Write(data); err != nil {
		return fmt.Errorf("write frame metadata: %w", err)
	}

	r.TrackingFrameCount++
	return nil
}

func (r *EventRecorder) closeTrackingStream() error {
	if r.TrackingFile == nil {
		r.TrackingStreamOpen = false
		return nil
	}

	var errs []error
	if r.TrackingStreamOpen {
		if _, err := r.TrackingFile.WriteString("]}"); err != nil {
			errs = append(errs, fmt.Errorf("finalize tracking metadata: %w", err))
		}
		r.TrackingStreamOpen = false
	}
	if err := r.TrackingFile.Close(); err != nil {
		errs = append(errs, err)
	}
	r.TrackingFile = nil

	return errors.Join(errs...)
}

func (r *EventRecorder) closeWriters() error {
	var errs []error

	if r.RawWriter != nil {
		if err := r.RawWriter.Close(); err != nil {
			errs = append(errs, err)
		}
		r.RawWriter = nil
	}
	if r.TrackedWriter != nil {
		if err := r.TrackedWriter.Close(); err != nil {
			errs = append(errs, err)
		}
		r.TrackedWriter = nil
	}
	if r.MaskedWriter != nil {
		if err := r.MaskedWriter.Close(); err != nil {
			errs = append(errs, err)
		}
		r.MaskedWriter = nil
	}

	return errors.Join(errs...)
}
