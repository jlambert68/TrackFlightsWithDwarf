//go:build opencv

package engine

import (
	"image"
	"image/color"

	"gocv.io/x/gocv"
)

func trackingROIForSize(width, height int, settings TrackingSettings) image.Rectangle {
	if width <= 0 || height <= 0 {
		return image.Rectangle{}
	}

	roiHeight := int(float64(height) * settings.TrackingROIHeightFrac)
	if roiHeight <= 0 || roiHeight >= height {
		return image.Rect(0, 0, width, height)
	}

	marginY := (height - roiHeight) / 2
	return image.Rect(0, marginY, width, marginY+roiHeight)
}

func applyTrackingROI(mask *gocv.Mat, roi image.Rectangle) {
	bounds := image.Rect(0, 0, mask.Cols(), mask.Rows())
	if roi.Empty() || roi == bounds {
		return
	}

	if roi.Min.Y > bounds.Min.Y {
		gocv.Rectangle(mask, image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Max.X, roi.Min.Y), color.RGBA{}, -1)
	}
	if roi.Max.Y < bounds.Max.Y {
		gocv.Rectangle(mask, image.Rect(bounds.Min.X, roi.Max.Y, bounds.Max.X, bounds.Max.Y), color.RGBA{}, -1)
	}
	if roi.Min.X > bounds.Min.X {
		gocv.Rectangle(mask, image.Rect(bounds.Min.X, roi.Min.Y, roi.Min.X, roi.Max.Y), color.RGBA{}, -1)
	}
	if roi.Max.X < bounds.Max.X {
		gocv.Rectangle(mask, image.Rect(roi.Max.X, roi.Min.Y, bounds.Max.X, roi.Max.Y), color.RGBA{}, -1)
	}
}

func filterTracksToROI(tracks []*Track, roi image.Rectangle) []*Track {
	if roi.Empty() {
		return tracks
	}

	filtered := tracks[:0]
	for _, track := range tracks {
		if track != nil && track.Position.In(roi) {
			filtered = append(filtered, track)
		}
	}
	return filtered
}

func drawTrackingROI(frame *gocv.Mat, settings TrackingSettings) {
	roi := trackingROIForSize(frame.Cols(), frame.Rows(), settings)
	if roi.Empty() {
		return
	}
	gocv.Rectangle(frame, roi, roiColor, 2)
}
