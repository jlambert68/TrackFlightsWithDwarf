//go:build opencv

package engine

import (
	"image"
	"math"
	"time"

	"gocv.io/x/gocv"
)

const maxMetadataTrailPoints = 120

func maxMissedFramesForSettings(settings TrackingSettings) int {
	if settings.Profile == trackingProfileBall {
		return 12
	}
	return 8
}

func maxMissedFramesForTrack(track *Track, settings TrackingSettings) int {
	if settings.Profile == trackingProfileBall || (track != nil && track.Speed >= settings.MinSpeed) {
		return 12
	}
	return maxMissedFramesForSettings(settings)
}

// findDetections converts the cleaned foreground mask into bounding boxes and
// centers that can be handed to the tracker.
func findDetections(mask gocv.Mat, settings TrackingSettings) []Detection {
	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	detections := make([]Detection, 0, contours.Size())

	for i := 0; i < contours.Size(); i++ {
		contour := contours.At(i)
		area := gocv.ContourArea(contour)

		if area < settings.MinArea || area > settings.MaxArea {
			continue
		}

		rect := gocv.BoundingRect(contour)
		if rect.Dx() < 2 || rect.Dy() < 2 {
			continue
		}
		rectArea := float64(rect.Dx() * rect.Dy())
		if rectArea <= 0 {
			continue
		}
		if area/rectArea < minDetectionFillRatioForSettings(settings) {
			continue
		}

		detections = append(detections, Detection{
			Rect: rect,
			Center: image.Pt(
				(rect.Min.X+rect.Max.X)/2,
				(rect.Min.Y+rect.Max.Y)/2,
			),
			Area: area,
		})
	}

	return mergeNearbyDetections(detections, settings)
}

func mergeNearbyDetections(detections []Detection, settings TrackingSettings) []Detection {
	if len(detections) < 2 {
		return detections
	}

	merged := append([]Detection(nil), detections...)
	for {
		changed := false

		for i := 0; i < len(merged); i++ {
			for j := i + 1; j < len(merged); j++ {
				if !shouldMergeDetections(merged[i], merged[j], settings) {
					continue
				}

				merged[i] = mergeDetectionPair(merged[i], merged[j])
				merged = append(merged[:j], merged[j+1:]...)
				changed = true
				break
			}
			if changed {
				break
			}
		}

		if !changed {
			return merged
		}
	}
}

func shouldMergeDetections(a, b Detection, settings TrackingSettings) bool {
	aRect := a.Rect
	bRect := b.Rect
	if aRect.Empty() || bRect.Empty() {
		return false
	}

	hGap := rectAxisGap(aRect.Min.X, aRect.Max.X, bRect.Min.X, bRect.Max.X)
	vGap := rectAxisGap(aRect.Min.Y, aRect.Max.Y, bRect.Min.Y, bRect.Max.Y)

	maxWidth := maxInt(aRect.Dx(), bRect.Dx())
	maxHeight := maxInt(aRect.Dy(), bRect.Dy())

	// Nearby mask fragments from one aircraft should be fused into one
	// detection before track association, otherwise the recorder saves partial
	// crops for separate track IDs.
	maxHorizontalGap := maxInt(24, maxWidth/2)
	maxVerticalGap := maxInt(12, maxHeight)

	if hGap > maxHorizontalGap || vGap > maxVerticalGap {
		return false
	}

	union := aRect.Union(bRect)
	if float64(union.Dx()*union.Dy()) > settings.MaxArea*1.5 {
		return false
	}

	return true
}

func mergeDetectionPair(a, b Detection) Detection {
	rect := a.Rect.Union(b.Rect)
	return Detection{
		Rect: rect,
		Center: image.Pt(
			(rect.Min.X+rect.Max.X)/2,
			(rect.Min.Y+rect.Max.Y)/2,
		),
		Area: a.Area + b.Area,
	}
}

func rectAxisGap(aMin, aMax, bMin, bMax int) int {
	if aMax < bMin {
		return bMin - aMax
	}
	if bMax < aMin {
		return aMin - bMax
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// updateTracks matches detections to existing tracks by nearest predicted
// position, updates velocity estimates, creates new tracks, and keeps stale
// tracks available for display after detections disappear.
func updateTracks(
	tracks []*Track,
	detections []Detection,
	now time.Time,
	frameDT float64,
	nextTrackID int,
	settings TrackingSettings,
) ([]*Track, int) {
	detectionUsed := make([]bool, len(detections))
	trackMatched := make([]bool, len(tracks))

	for {
		bestTrack := -1
		bestDetection := -1
		bestScore := math.MaxFloat64

		// Prefer detections that stay close to the predicted motion, overlap the
		// previous box, and keep roughly the same scale. This keeps one aircraft
		// from hopping to a nearby fragment or noise blob when the mask wobbles.
		for ti, track := range tracks {
			if trackMatched[ti] {
				continue
			}

			dt := now.Sub(track.LastUpdate).Seconds()
			if dt <= 0 || dt > 1 {
				dt = frameDT
			}
			if settings.Profile == trackingProfileBall {
				dt = frameDT
			}

			predictedX := float64(track.Position.X) + track.VX*dt
			predictedY := float64(track.Position.Y) + track.VY*dt

			for di, detection := range detections {
				if detectionUsed[di] {
					continue
				}

				score, ok := scoreTrackDetectionMatch(track, detection, predictedX, predictedY, settings)
				if ok && score < bestScore {
					bestScore = score
					bestTrack = ti
					bestDetection = di
				}
			}
		}

		if bestTrack == -1 {
			break
		}

		track := tracks[bestTrack]
		detection := detections[bestDetection]

		dt := now.Sub(track.LastUpdate).Seconds()
		if dt <= 0 || dt > 1 {
			dt = frameDT
		}

		oldPosition := track.Position
		dx := float64(detection.Center.X - oldPosition.X)
		dy := float64(detection.Center.Y - oldPosition.Y)

		measuredVX := dx / dt
		measuredVY := dy / dt

		const velocityAlpha = 0.45
		if track.Hits <= 1 {
			track.VX = measuredVX
			track.VY = measuredVY
		} else {
			track.VX = track.VX*(1-velocityAlpha) + measuredVX*velocityAlpha
			track.VY = track.VY*(1-velocityAlpha) + measuredVY*velocityAlpha
		}

		track.Speed = math.Hypot(track.VX, track.VY)
		track.PrevPosition = oldPosition
		track.Position = detection.Center
		track.Rect = detection.Rect
		track.LastUpdate = now
		track.Hits++
		track.Missed = 0

		track.Trail = append(track.Trail, detection.Center)

		trackMatched[bestTrack] = true
		detectionUsed[bestDetection] = true
	}

	for i, track := range tracks {
		if !trackMatched[i] {
			track.Missed++
			if settings.Profile == trackingProfileBall {
				track.VX *= 0.35
				track.VY *= 0.35
			}
		}
	}

	activeTracks := tracks[:0]
	for _, track := range tracks {
		if track.Missed > maxMissedFramesForTrack(track, settings) {
			continue
		}
		activeTracks = append(activeTracks, track)
	}
	tracks = activeTracks

	for i, detection := range detections {
		if detectionUsed[i] {
			continue
		}

		tracks = append(tracks, &Track{
			ID:           nextTrackID,
			Rect:         detection.Rect,
			Position:     detection.Center,
			PrevPosition: detection.Center,
			LastUpdate:   now,
			Hits:         1,
			Trail:        []image.Point{detection.Center},
		})
		nextTrackID++
	}

	return tracks, nextTrackID
}

func scoreTrackDetectionMatch(track *Track, detection Detection, predictedX, predictedY float64, settings TrackingSettings) (float64, bool) {
	dx := float64(detection.Center.X) - predictedX
	dy := float64(detection.Center.Y) - predictedY
	distance := math.Hypot(dx, dy)
	matchDistance := trackDetectionMatchDistance(track.Rect, detection.Rect, settings.MaxMatchDistance)
	if distance > matchDistance {
		return 0, false
	}

	trackArea := float64(track.Rect.Dx() * track.Rect.Dy())
	detectionArea := float64(detection.Rect.Dx() * detection.Rect.Dy())
	if trackArea <= 0 || detectionArea <= 0 {
		return 0, false
	}

	scaleRatio := math.Sqrt(detectionArea / trackArea)
	if scaleRatio < 1 {
		scaleRatio = 1 / scaleRatio
	}

	iou := rectIOU(track.Rect, detection.Rect)
	fastTransient := settings.Profile == trackingProfileBall || track.Speed >= settings.MinSpeed

	// Once a track is established, reject candidates whose size changes too
	// violently unless the boxes still overlap enough to justify the jump.
	if !fastTransient && track.Hits >= settings.MinHits*2 && scaleRatio > 3.0 && iou < 0.08 {
		return 0, false
	}

	distanceScore := distance / matchDistance
	sizePenalty := math.Max(0, scaleRatio-1.0) * 0.20
	overlapBonus := iou * 0.85
	if fastTransient {
		if track.Hits >= settings.MinHits && scaleRatio > 8.0 && iou < 0.04 {
			return 0, false
		}
		sizePenalty = math.Max(0, scaleRatio-1.0) * 0.12
		overlapBonus = iou * 1.15
	}

	return distanceScore + sizePenalty - overlapBonus, true
}

func trackDetectionMatchDistance(trackRect, detectionRect image.Rectangle, configuredDistance float64) float64 {
	trackDiagonal := math.Hypot(float64(trackRect.Dx()), float64(trackRect.Dy()))
	detectionDiagonal := math.Hypot(float64(detectionRect.Dx()), float64(detectionRect.Dy()))
	objectRadius := math.Max(trackDiagonal, detectionDiagonal) / 2

	// MaxMatchDistance remains the base travel allowance. Add up to one more
	// base distance for the object's radius because a large, blurred ball can
	// move its detected center farther between frames than a small aircraft.
	// The cap prevents a large foreground region from matching across the
	// entire image.
	return configuredDistance + math.Min(configuredDistance, objectRadius)
}

func rectIOU(a, b image.Rectangle) float64 {
	intersection := a.Intersect(b)
	if intersection.Empty() {
		return 0
	}

	intersectionArea := float64(intersection.Dx() * intersection.Dy())
	aArea := float64(a.Dx() * a.Dy())
	bArea := float64(b.Dx() * b.Dy())
	unionArea := aArea + bArea - intersectionArea
	if unionArea <= 0 {
		return 0
	}

	return intersectionArea / unionArea
}

// makeFrameMetadata filters the active tracks down to the ones considered
// stable and interesting enough to persist.
func makeFrameMetadata(sourceFrame int, start, now time.Time, tracks []*Track, settings TrackingSettings) FrameMetadata {
	meta := FrameMetadata{
		SourceFrame: sourceFrame,
		TimeUnixNS:  now.UnixNano(),
		TimeMS:      now.Sub(start).Milliseconds(),
		Tracks:      make([]TrackMetadata, 0),
	}

	for _, t := range tracks {
		if t.Missed > 0 {
			continue
		}

		trackType, ok := classifyTrack(t, settings)
		if t.Hits < settings.MinHits || !ok {
			continue
		}

		meta.Tracks = append(meta.Tracks, TrackMetadata{
			ID:        t.ID,
			Type:      trackType,
			X:         t.Position.X,
			Y:         t.Position.Y,
			BoxX:      t.Rect.Min.X,
			BoxY:      t.Rect.Min.Y,
			BoxWidth:  t.Rect.Dx(),
			BoxHeight: t.Rect.Dy(),
			VX:        t.VX,
			VY:        t.VY,
			Speed:     t.Speed,
			Trail:     makeTrailMetadata(t.Trail),
		})
	}

	return meta
}

func makeTrailMetadata(points []image.Point) []TrailPoint {
	if len(points) == 0 {
		return nil
	}
	if len(points) > maxMetadataTrailPoints {
		points = points[len(points)-maxMetadataTrailPoints:]
	}

	trail := make([]TrailPoint, len(points))
	for i, point := range points {
		trail[i] = TrailPoint{X: point.X, Y: point.Y}
	}
	return trail
}

func classifyTrack(t *Track, settings TrackingSettings) (string, bool) {
	if t == nil {
		return "", false
	}
	if t.Hits < minConfirmedHitsForClassification(settings) {
		return "", false
	}
	if len(t.Trail) < minTrackTrailPointsForSettings(settings) {
		return "", false
	}
	if trackNetDisplacement(t.Trail) < minTrackNetDisplacementForSettings(settings) {
		return "", false
	}
	if t.Speed >= settings.MinSpeed {
		return trackTypeFast, true
	}
	if t.Speed >= settings.SlowMinSpeed {
		return trackTypeSlow, true
	}
	return "", false
}

func minDetectionFillRatioForSettings(settings TrackingSettings) float64 {
	if settings.Profile == trackingProfileBall {
		return 0.10
	}
	return 0.20
}

func minConfirmedHitsForClassification(settings TrackingSettings) int {
	if settings.MinHits > 2 {
		return settings.MinHits
	}
	// Two points are the minimum needed to measure real displacement. Honor a
	// configured MinHits of 2 so a short, fast crossing is not silently held
	// to the general profile's default of 3.
	return 2
}

func minTrackTrailPointsForSettings(settings TrackingSettings) int {
	return minConfirmedHitsForClassification(settings)
}

func minTrackNetDisplacementForSettings(settings TrackingSettings) float64 {
	if settings.Profile == trackingProfileBall {
		return 4.0
	}
	return 10.0
}

func trackNetDisplacement(points []image.Point) float64 {
	if len(points) < 2 {
		return 0
	}
	first := points[0]
	last := points[len(points)-1]
	return math.Hypot(float64(last.X-first.X), float64(last.Y-first.Y))
}

func hasFreshInterestingTracks(tracks []*Track, settings TrackingSettings) bool {
	for _, track := range tracks {
		trackType, ok := classifyTrack(track, settings)
		if ok && (trackType == trackTypeFast || trackType == trackTypeSlow) && track.Missed == 0 {
			return true
		}
	}
	return false
}

func countTrackTypes(tracks []TrackMetadata) (fastCount, slowCount int) {
	for _, track := range tracks {
		switch track.Type {
		case trackTypeFast:
			fastCount++
		case trackTypeSlow:
			slowCount++
		}
	}
	return fastCount, slowCount
}
