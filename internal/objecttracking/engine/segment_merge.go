//go:build opencv

package engine

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sort"
	"time"
)

type SegmentTrackAssignment struct {
	SegmentIndex  int `json:"segment_index"`
	LocalTrackID  int `json:"local_track_id"`
	GlobalTrackID int `json:"global_track_id"`
}

type SegmentBoundaryMatch struct {
	LeftSegmentIndex  int     `json:"left_segment_index"`
	RightSegmentIndex int     `json:"right_segment_index"`
	LeftTrackID       int     `json:"left_track_id"`
	RightTrackID      int     `json:"right_track_id"`
	SharedFrames      int     `json:"shared_frames"`
	Score             float64 `json:"score"`
}

type MergedSegmentTracking struct {
	Metadata      EventMetadata            `json:"metadata"`
	Assignments   []SegmentTrackAssignment `json:"assignments"`
	BoundaryMatch []SegmentBoundaryMatch   `json:"boundary_matches"`
}

type segmentTrackObservation struct {
	Frame FrameMetadata
	Track TrackMetadata
}

type segmentTrackCandidate struct {
	leftTrackID  int
	rightTrackID int
	sharedFrames int
	score        float64
}

type segmentTrackKey struct {
	segmentIndex int
	trackID      int
}

type frameMergeState struct {
	sourceFrame int
	timeMS      int64
	meanLuma    float64
	tracks      map[int]TrackMetadata
}

func MergeSegmentTrackingResults(manifest RawSegmentManifest, segments []EventMetadata, settings TrackingSettings) (MergedSegmentTracking, error) {
	if len(manifest.Segments) == 0 {
		return MergedSegmentTracking{}, errors.New("raw segment manifest has no segments")
	}
	if len(segments) != len(manifest.Segments) {
		return MergedSegmentTracking{}, fmt.Errorf("segment manifest contains %d segments but %d tracking payloads were provided", len(manifest.Segments), len(segments))
	}

	settings = NormalizeTrackingSettings(settings)
	globalized := make([]EventMetadata, len(segments))
	for i := range segments {
		globalized[i] = globalizeSegmentMetadata(manifest.Segments[i], segments[i], manifest.CreatedAt, manifest.Capture)
	}

	roots := newSegmentTrackUnion(globalized)
	boundaryMatches := make([]SegmentBoundaryMatch, 0)
	for i := 0; i+1 < len(globalized); i++ {
		matches := matchSegmentBoundary(manifest.Segments[i], globalized[i], manifest.Segments[i+1], globalized[i+1], settings)
		for _, match := range matches {
			left := segmentTrackKey{segmentIndex: i, trackID: match.leftTrackID}
			right := segmentTrackKey{segmentIndex: i + 1, trackID: match.rightTrackID}
			roots.union(left, right)
			boundaryMatches = append(boundaryMatches, SegmentBoundaryMatch{
				LeftSegmentIndex:  i,
				RightSegmentIndex: i + 1,
				LeftTrackID:       match.leftTrackID,
				RightTrackID:      match.rightTrackID,
				SharedFrames:      match.sharedFrames,
				Score:             match.score,
			})
		}
	}

	globalIDs := assignGlobalTrackIDs(globalized, roots)
	assignments := make([]SegmentTrackAssignment, 0, len(globalIDs))
	for key, globalID := range globalIDs {
		assignments = append(assignments, SegmentTrackAssignment{
			SegmentIndex:  key.segmentIndex,
			LocalTrackID:  key.trackID,
			GlobalTrackID: globalID,
		})
	}
	sort.Slice(assignments, func(i, j int) bool {
		if assignments[i].SegmentIndex != assignments[j].SegmentIndex {
			return assignments[i].SegmentIndex < assignments[j].SegmentIndex
		}
		return assignments[i].LocalTrackID < assignments[j].LocalTrackID
	})

	merged := mergeGlobalizedFrames(globalized, globalIDs, manifest.CreatedAt)
	return MergedSegmentTracking{
		Metadata:      merged,
		Assignments:   assignments,
		BoundaryMatch: boundaryMatches,
	}, nil
}

func globalizeSegmentMetadata(segment RawVideoSegment, metadata EventMetadata, startedAt time.Time, capture CaptureMetadata) EventMetadata {
	frames := make([]FrameMetadata, 0, len(metadata.Frames))
	for _, frame := range metadata.Frames {
		globalFrame := frame
		if globalFrame.SourceFrame <= 0 {
			globalFrame.SourceFrame = segment.StartFrame
		} else {
			globalFrame.SourceFrame = segment.StartFrame + globalFrame.SourceFrame - 1
		}
		globalFrame.TimeMS = segment.StartTimeMS + frame.TimeMS
		if !startedAt.IsZero() {
			globalFrame.TimeUnixNS = startedAt.Add(time.Duration(globalFrame.TimeMS) * time.Millisecond).UnixNano()
		} else {
			globalFrame.TimeUnixNS = 0
		}
		frames = append(frames, globalFrame)
	}
	if metadata.Capture == (CaptureMetadata{}) {
		metadata.Capture = capture
	}

	return EventMetadata{
		EventID:   metadata.EventID,
		StartedAt: startedAt,
		FPS:       metadata.FPS,
		Width:     metadata.Width,
		Height:    metadata.Height,
		Capture:   metadata.Capture,
		Frames:    frames,
	}
}

func matchSegmentBoundary(leftSegment RawVideoSegment, left EventMetadata, rightSegment RawVideoSegment, right EventMetadata, settings TrackingSettings) []segmentTrackCandidate {
	overlapStart := maxInt(leftSegment.StartFrame, rightSegment.StartFrame)
	overlapEnd := minInt(leftSegment.EndFrame, rightSegment.EndFrame)
	if overlapStart > overlapEnd {
		return nil
	}

	leftObs := collectSegmentObservations(left, overlapStart, overlapEnd)
	rightObs := collectSegmentObservations(right, overlapStart, overlapEnd)
	if len(leftObs) == 0 || len(rightObs) == 0 {
		return nil
	}

	candidates := make([]segmentTrackCandidate, 0)
	for leftTrackID, leftFrames := range leftObs {
		for rightTrackID, rightFrames := range rightObs {
			score, sharedFrames, ok := scoreSegmentBoundaryCandidate(leftFrames, rightFrames, settings)
			if !ok {
				continue
			}
			candidates = append(candidates, segmentTrackCandidate{
				leftTrackID:  leftTrackID,
				rightTrackID: rightTrackID,
				sharedFrames: sharedFrames,
				score:        score,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score < candidates[j].score
		}
		if candidates[i].sharedFrames != candidates[j].sharedFrames {
			return candidates[i].sharedFrames > candidates[j].sharedFrames
		}
		if candidates[i].leftTrackID != candidates[j].leftTrackID {
			return candidates[i].leftTrackID < candidates[j].leftTrackID
		}
		return candidates[i].rightTrackID < candidates[j].rightTrackID
	})

	leftUsed := make(map[int]bool, len(leftObs))
	rightUsed := make(map[int]bool, len(rightObs))
	selected := make([]segmentTrackCandidate, 0)
	for _, candidate := range candidates {
		if leftUsed[candidate.leftTrackID] || rightUsed[candidate.rightTrackID] {
			continue
		}
		leftUsed[candidate.leftTrackID] = true
		rightUsed[candidate.rightTrackID] = true
		selected = append(selected, candidate)
	}
	return selected
}

func collectSegmentObservations(metadata EventMetadata, startFrame, endFrame int) map[int][]segmentTrackObservation {
	observations := make(map[int][]segmentTrackObservation)
	for _, frame := range metadata.Frames {
		if frame.SourceFrame < startFrame || frame.SourceFrame > endFrame {
			continue
		}
		for _, track := range frame.Tracks {
			observations[track.ID] = append(observations[track.ID], segmentTrackObservation{
				Frame: frame,
				Track: track,
			})
		}
	}
	return observations
}

func scoreSegmentBoundaryCandidate(leftObs, rightObs []segmentTrackObservation, settings TrackingSettings) (float64, int, bool) {
	if len(leftObs) == 0 || len(rightObs) == 0 {
		return 0, 0, false
	}

	leftByFrame := make(map[int]segmentTrackObservation, len(leftObs))
	for _, obs := range leftObs {
		leftByFrame[obs.Frame.SourceFrame] = obs
	}
	rightByFrame := make(map[int]segmentTrackObservation, len(rightObs))
	for _, obs := range rightObs {
		rightByFrame[obs.Frame.SourceFrame] = obs
	}

	sharedFrames := 0
	totalDistance := 0.0
	totalIOU := 0.0
	totalSpeedDelta := 0.0
	for frame, left := range leftByFrame {
		right, ok := rightByFrame[frame]
		if !ok {
			continue
		}
		sharedFrames++
		totalDistance += trackCenterDistance(left.Track, right.Track)
		totalIOU += rectIOU(trackRect(left.Track), trackRect(right.Track))
		totalSpeedDelta += math.Abs(left.Track.Speed - right.Track.Speed)
	}

	leftLast := leftObs[len(leftObs)-1]
	rightFirst := rightObs[0]
	predictedDistance := trackPredictionDistance(leftLast, rightFirst)
	maxDistance := settings.MaxMatchDistance * 1.35
	if predictedDistance > maxDistance {
		return 0, sharedFrames, false
	}

	sizeRatio := trackAreaRatio(leftLast.Track, rightFirst.Track)
	averageIOU := 0.0
	averageDistance := predictedDistance
	averageSpeedDelta := math.Abs(leftLast.Track.Speed - rightFirst.Track.Speed)
	if sharedFrames > 0 {
		averageIOU = totalIOU / float64(sharedFrames)
		averageDistance = totalDistance / float64(sharedFrames)
		averageSpeedDelta = totalSpeedDelta / float64(sharedFrames)
	}
	if sizeRatio > 4.0 && averageIOU < 0.08 {
		return 0, sharedFrames, false
	}

	score := averageDistance/settings.MaxMatchDistance +
		math.Max(0, sizeRatio-1.0)*0.20 +
		averageSpeedDelta/maxFloat(1, settings.MinSpeed)*0.05 -
		averageIOU*0.85
	if sharedFrames > 0 {
		score -= math.Min(float64(sharedFrames), 3) * 0.08
	}
	if score > 1.15 {
		return 0, sharedFrames, false
	}

	return score, sharedFrames, true
}

func trackPredictionDistance(left, right segmentTrackObservation) float64 {
	dtMS := right.Frame.TimeMS - left.Frame.TimeMS
	dtSeconds := float64(dtMS) / 1000.0
	if dtSeconds < 0 {
		dtSeconds = 0
	}
	predictedX := float64(left.Track.X) + left.Track.VX*dtSeconds
	predictedY := float64(left.Track.Y) + left.Track.VY*dtSeconds
	dx := float64(right.Track.X) - predictedX
	dy := float64(right.Track.Y) - predictedY
	return math.Hypot(dx, dy)
}

func trackCenterDistance(left, right TrackMetadata) float64 {
	dx := float64(right.X - left.X)
	dy := float64(right.Y - left.Y)
	return math.Hypot(dx, dy)
}

func trackAreaRatio(left, right TrackMetadata) float64 {
	leftArea := float64(trackRect(left).Dx() * trackRect(left).Dy())
	rightArea := float64(trackRect(right).Dx() * trackRect(right).Dy())
	if leftArea <= 0 || rightArea <= 0 {
		return math.Inf(1)
	}
	ratio := rightArea / leftArea
	if ratio < 1 {
		return 1 / ratio
	}
	return ratio
}

func trackRect(track TrackMetadata) image.Rectangle {
	return image.Rect(track.BoxX, track.BoxY, track.BoxX+track.BoxWidth, track.BoxY+track.BoxHeight)
}

func mergeGlobalizedFrames(segments []EventMetadata, globalIDs map[segmentTrackKey]int, startedAt time.Time) EventMetadata {
	frames := make(map[int]*frameMergeState)
	fps := 0.0
	width := 0
	height := 0
	eventID := ""
	capture := CaptureMetadata{}

	for segmentIndex, metadata := range segments {
		if eventID == "" {
			eventID = metadata.EventID
		}
		if capture == (CaptureMetadata{}) && metadata.Capture != (CaptureMetadata{}) {
			capture = metadata.Capture
		}
		if fps <= 0 {
			fps = metadata.FPS
		}
		if width <= 0 {
			width = metadata.Width
		}
		if height <= 0 {
			height = metadata.Height
		}

		for _, frame := range metadata.Frames {
			state := frames[frame.SourceFrame]
			if state == nil {
				state = &frameMergeState{
					sourceFrame: frame.SourceFrame,
					timeMS:      frame.TimeMS,
					meanLuma:    frame.MeanLuma,
					tracks:      make(map[int]TrackMetadata),
				}
				frames[frame.SourceFrame] = state
			} else if frame.TimeMS < state.timeMS {
				state.timeMS = frame.TimeMS
			}

			for _, track := range frame.Tracks {
				globalID := globalIDs[segmentTrackKey{segmentIndex: segmentIndex, trackID: track.ID}]
				track.ID = globalID
				if existing, ok := state.tracks[globalID]; ok {
					state.tracks[globalID] = mergeTrackMetadata(existing, track)
					continue
				}
				state.tracks[globalID] = track
			}
		}
	}

	frameNumbers := make([]int, 0, len(frames))
	for frame := range frames {
		frameNumbers = append(frameNumbers, frame)
	}
	sort.Ints(frameNumbers)

	mergedFrames := make([]FrameMetadata, 0, len(frameNumbers))
	for _, sourceFrame := range frameNumbers {
		state := frames[sourceFrame]
		tracks := make([]TrackMetadata, 0, len(state.tracks))
		for _, track := range state.tracks {
			tracks = append(tracks, track)
		}
		sort.Slice(tracks, func(i, j int) bool {
			return tracks[i].ID < tracks[j].ID
		})

		frame := FrameMetadata{
			SourceFrame: sourceFrame,
			TimeMS:      state.timeMS,
			MeanLuma:    state.meanLuma,
			Tracks:      tracks,
		}
		if !startedAt.IsZero() {
			frame.TimeUnixNS = startedAt.Add(time.Duration(frame.TimeMS) * time.Millisecond).UnixNano()
		}
		mergedFrames = append(mergedFrames, frame)
	}

	return EventMetadata{
		EventID:   eventID,
		StartedAt: startedAt,
		FPS:       fps,
		Width:     width,
		Height:    height,
		Capture:   capture,
		Frames:    mergedFrames,
	}
}

func mergeTrackMetadata(left, right TrackMetadata) TrackMetadata {
	merged := left
	if right.Type == trackTypeFast {
		merged.Type = right.Type
	}
	if right.Speed > merged.Speed {
		merged.X = right.X
		merged.Y = right.Y
		merged.BoxX = right.BoxX
		merged.BoxY = right.BoxY
		merged.BoxWidth = right.BoxWidth
		merged.BoxHeight = right.BoxHeight
		merged.VX = right.VX
		merged.VY = right.VY
		merged.Speed = right.Speed
	}
	if len(right.Trail) > len(merged.Trail) {
		merged.Trail = right.Trail
	}
	return merged
}

type segmentTrackUnion struct {
	parent map[segmentTrackKey]segmentTrackKey
}

func newSegmentTrackUnion(segments []EventMetadata) *segmentTrackUnion {
	parent := make(map[segmentTrackKey]segmentTrackKey)
	for segmentIndex, metadata := range segments {
		seen := make(map[int]struct{})
		for _, frame := range metadata.Frames {
			for _, track := range frame.Tracks {
				if _, ok := seen[track.ID]; ok {
					continue
				}
				seen[track.ID] = struct{}{}
				key := segmentTrackKey{segmentIndex: segmentIndex, trackID: track.ID}
				parent[key] = key
			}
		}
	}
	return &segmentTrackUnion{parent: parent}
}

func (u *segmentTrackUnion) find(key segmentTrackKey) segmentTrackKey {
	parent, ok := u.parent[key]
	if !ok {
		u.parent[key] = key
		return key
	}
	if parent == key {
		return key
	}
	root := u.find(parent)
	u.parent[key] = root
	return root
}

func (u *segmentTrackUnion) union(left, right segmentTrackKey) {
	leftRoot := u.find(left)
	rightRoot := u.find(right)
	if leftRoot == rightRoot {
		return
	}
	if compareSegmentTrackKey(leftRoot, rightRoot) <= 0 {
		u.parent[rightRoot] = leftRoot
		return
	}
	u.parent[leftRoot] = rightRoot
}

func assignGlobalTrackIDs(segments []EventMetadata, union *segmentTrackUnion) map[segmentTrackKey]int {
	globalIDs := make(map[segmentTrackKey]int)
	rootIDs := make(map[segmentTrackKey]int)
	nextID := 1

	for segmentIndex, metadata := range segments {
		for _, frame := range metadata.Frames {
			for _, track := range frame.Tracks {
				key := segmentTrackKey{segmentIndex: segmentIndex, trackID: track.ID}
				root := union.find(key)
				globalID, ok := rootIDs[root]
				if !ok {
					globalID = nextID
					nextID++
					rootIDs[root] = globalID
				}
				globalIDs[key] = globalID
			}
		}
	}
	return globalIDs
}

func compareSegmentTrackKey(left, right segmentTrackKey) int {
	if left.segmentIndex != right.segmentIndex {
		return left.segmentIndex - right.segmentIndex
	}
	return left.trackID - right.trackID
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
