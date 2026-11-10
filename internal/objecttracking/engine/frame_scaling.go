//go:build opencv

package engine

import (
	"image"
	"math"

	"gocv.io/x/gocv"
)

const (
	defaultAnalysisMaxWidth = 1920
	defaultPreviewMaxWidth  = 960
	defaultPreviewFPS       = 10.0
)

// scaledFrameSize preserves the source aspect ratio and only downsizes.
func scaledFrameSize(width, height, maxWidth int) (scaledWidth, scaledHeight int, scale float64) {
	if width <= 0 || height <= 0 || maxWidth <= 0 || width <= maxWidth {
		return width, height, 1
	}

	scale = float64(maxWidth) / float64(width)
	scaledHeight = max(1, int(math.Round(float64(height)*scale)))
	return maxWidth, scaledHeight, scale
}

func frameForAnalysis(frame gocv.Mat, resized *gocv.Mat, maxWidth int) (gocv.Mat, float64, error) {
	width, height, scale := scaledFrameSize(frame.Cols(), frame.Rows(), maxWidth)
	if scale >= 1 {
		return frame, 1, nil
	}
	gocv.Resize(frame, resized, image.Pt(width, height), 0, 0, gocv.InterpolationArea)
	return *resized, scale, nil
}

// trackingSettingsForScale keeps thresholds expressed in source-image pixels
// while contours are extracted from a smaller analysis image.
func trackingSettingsForScale(settings TrackingSettings, scale float64) TrackingSettings {
	if scale <= 0 || scale >= 1 {
		return settings
	}

	scaled := settings
	scaled.MinArea *= scale * scale
	scaled.MaxArea *= scale * scale
	scaled.MaxMatchDistance *= scale
	scaled.SlowMinSpeed *= scale
	scaled.MinSpeed *= scale
	return scaled
}

func scaleDetections(detections []Detection, scale float64) []Detection {
	if scale <= 0 || scale == 1 {
		return detections
	}

	for i := range detections {
		detections[i].Rect = scaleRectangle(detections[i].Rect, scale)
		detections[i].Center = scalePoint(detections[i].Center, scale)
		detections[i].Area *= scale * scale
	}
	return detections
}

func scaleTrackMetadata(tracks []TrackMetadata, scale float64) []TrackMetadata {
	if len(tracks) == 0 || scale <= 0 || scale == 1 {
		return tracks
	}

	scaled := make([]TrackMetadata, len(tracks))
	for i, track := range tracks {
		scaled[i] = track
		scaled[i].X = scaleInt(track.X, scale)
		scaled[i].Y = scaleInt(track.Y, scale)
		scaled[i].BoxX = scaleInt(track.BoxX, scale)
		scaled[i].BoxY = scaleInt(track.BoxY, scale)
		scaled[i].BoxWidth = max(1, scaleInt(track.BoxWidth, scale))
		scaled[i].BoxHeight = max(1, scaleInt(track.BoxHeight, scale))
		scaled[i].VX = track.VX * scale
		scaled[i].VY = track.VY * scale
		if len(track.Trail) > 0 {
			scaled[i].Trail = make([]TrailPoint, len(track.Trail))
			for pointIndex, point := range track.Trail {
				scaled[i].Trail[pointIndex] = TrailPoint{
					X: scaleInt(point.X, scale),
					Y: scaleInt(point.Y, scale),
				}
			}
		}
	}
	return scaled
}

func scaleRectangle(rect image.Rectangle, scale float64) image.Rectangle {
	if rect.Empty() || scale <= 0 || scale == 1 {
		return rect
	}
	minPoint := scalePoint(rect.Min, scale)
	maxPoint := scalePoint(rect.Max, scale)
	if maxPoint.X <= minPoint.X {
		maxPoint.X = minPoint.X + 1
	}
	if maxPoint.Y <= minPoint.Y {
		maxPoint.Y = minPoint.Y + 1
	}
	return image.Rectangle{Min: minPoint, Max: maxPoint}
}

func scalePoint(point image.Point, scale float64) image.Point {
	return image.Pt(scaleInt(point.X, scale), scaleInt(point.Y, scale))
}

func scaleInt(value int, scale float64) int {
	return int(math.Round(float64(value) * scale))
}

// previewFrameDue samples the source timeline, so a slowly processed 4K file
// does not fall back to rendering every decoded frame.
func previewFrameDue(sourceFrame int, sourceFPS, previewFPS float64) bool {
	if sourceFrame < 1 || previewFPS <= 0 {
		return false
	}
	if sourceFPS <= 0 || previewFPS >= sourceFPS {
		return true
	}
	stride := max(1, int(math.Ceil(sourceFPS/previewFPS)))
	return (sourceFrame-1)%stride == 0
}
