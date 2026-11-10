//go:build opencv

package engine

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"gocv.io/x/gocv"
)

// drawLiveOverlay renders the current accepted tracks onto the interactive live
// preview window.
func drawLiveOverlay(frame *gocv.Mat, tracks []*Track, settings TrackingSettings) {
	drawTrackingROI(frame, settings)

	for _, track := range tracks {
		trackType, ok := classifyTrack(track, settings)
		if track.Hits < settings.MinHits || !ok {
			continue
		}

		trackColor, arrowColor := overlayColors(trackType)
		gocv.Rectangle(frame, scaleRectAroundCenter(track.Rect, trackBoxScale), trackColor, 1)

		if len(track.Trail) >= 2 {
			for i := 1; i < len(track.Trail); i++ {
				gocv.Line(frame, track.Trail[i-1], track.Trail[i], trackColor, 1)
			}
		}

		drawTargetAndLabel(frame, track.ID, track.Position, track.VX, track.VY, track.Speed, trackColor, arrowColor)
	}
}

// drawMetadataOverlay reconstructs track trails from saved metadata so the
// tracked export video shows the same context as the live preview.
func drawMetadataOverlay(frame *gocv.Mat, tracks []TrackMetadata, settings TrackingSettings) {
	settings = NormalizeTrackingSettings(settings)
	drawTrackingROI(frame, settings)

	for _, t := range tracks {
		trackColor, arrowColor := overlayColors(t.Type)
		center := image.Pt(t.X, t.Y)

		rect := scaleRectAroundCenter(
			image.Rect(t.BoxX, t.BoxY, t.BoxX+t.BoxWidth, t.BoxY+t.BoxHeight),
			trackBoxScale,
		)
		gocv.Rectangle(frame, rect, trackColor, 1)

		trail := make([]image.Point, 0, len(t.Trail))
		for _, point := range t.Trail {
			trail = append(trail, image.Pt(point.X, point.Y))
		}

		for i := 1; i < len(trail); i++ {
			gocv.Line(frame, trail[i-1], trail[i], trackColor, 1)
		}

		drawTargetAndLabel(frame, t.ID, center, t.VX, t.VY, t.Speed, trackColor, arrowColor)
	}
}

func drawTrackInCropOverlay(frame *gocv.Mat, track TrackMetadata, rect image.Rectangle, center image.Point) {
	trackColor, arrowColor := overlayColors(track.Type)
	gocv.Rectangle(frame, rect, trackColor, 2)
	drawTargetAndLabel(frame, track.ID, center, track.VX, track.VY, track.Speed, trackColor, arrowColor)
}

func filterTrackMetadataByID(tracks []TrackMetadata, id int) []TrackMetadata {
	if id <= 0 || len(tracks) == 0 {
		return tracks
	}

	filtered := make([]TrackMetadata, 0, 1)
	for _, track := range tracks {
		if track.ID == id {
			filtered = append(filtered, track)
			break
		}
	}
	return filtered
}

func overlayColors(trackType string) (trackColor, arrowColor color.RGBA) {
	if trackType == trackTypeSlow {
		return slowTrackColor, slowArrowColor
	}
	return fastTrackColor, fastArrowColor
}

// scaleRectAroundCenter expands a rectangle around its center so the displayed
// tracking box is easier to see without changing the underlying track state.
func scaleRectAroundCenter(rect image.Rectangle, scale int) image.Rectangle {
	if scale <= 1 {
		return rect
	}

	width := rect.Dx()
	height := rect.Dy()
	if width <= 0 || height <= 0 {
		return rect
	}

	centerX := (rect.Min.X + rect.Max.X) / 2
	centerY := (rect.Min.Y + rect.Max.Y) / 2
	halfWidth := width * scale / 2
	halfHeight := height * scale / 2

	return image.Rect(
		centerX-halfWidth,
		centerY-halfHeight,
		centerX+halfWidth,
		centerY+halfHeight,
	)
}

// drawTargetAndLabel draws the crosshair, velocity arrow, and speed label for
// one target.
func drawTargetAndLabel(
	frame *gocv.Mat,
	id int,
	center image.Point,
	vx, vy, speed float64,
	trackColor, arrowColor color.RGBA,
) {
	gocv.Circle(frame, center, trackCircleRadius, trackColor, 2)
	gocv.Line(frame, image.Pt(center.X-trackCrosshairArm, center.Y), image.Pt(center.X+trackCrosshairArm, center.Y), trackColor, 1)
	gocv.Line(frame, image.Pt(center.X, center.Y-trackCrosshairArm), image.Pt(center.X, center.Y+trackCrosshairArm), trackColor, 1)

	const arrowSeconds = 0.20
	end := image.Pt(
		center.X+int(vx*arrowSeconds),
		center.Y+int(vy*arrowSeconds),
	)
	gocv.Line(frame, center, end, arrowColor, 2)

	label := fmt.Sprintf("#%d  %.0f px/s", id, speed)
	gocv.PutText(frame, label, overlayLabelPoint(frame, center, label, 1.2, 3),
		gocv.FontHersheySimplex, 1.2, trackColor, 3)
}

func overlayLabelPoint(frame *gocv.Mat, anchor image.Point, label string, scale float64, thickness int) image.Point {
	const (
		rightOffset = 18
		leftOffset  = 18
		yOffset     = 18
		margin      = 8
	)

	size := gocv.GetTextSize(label, gocv.FontHersheySimplex, scale, thickness)

	x := anchor.X + rightOffset
	if x+size.X+margin > frame.Cols() {
		x = anchor.X - leftOffset - size.X
	}
	x = overlayMaxInt(margin, overlayMinInt(x, frame.Cols()-size.X-margin))

	y := anchor.Y - yOffset
	minBaselineY := size.Y + margin
	maxBaselineY := frame.Rows() - margin
	y = overlayMaxInt(minBaselineY, overlayMinInt(y, maxBaselineY))

	return image.Pt(x, y)
}

func overlayMinInt(a, b int) int {
	return int(math.Min(float64(a), float64(b)))
}

func overlayMaxInt(a, b int) int {
	return int(math.Max(float64(a), float64(b)))
}
