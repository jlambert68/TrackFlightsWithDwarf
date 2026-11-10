//go:build opencv

package engine

import (
	"image"
	"image/color"
	"math"
	"os"
	"time"

	"gocv.io/x/gocv"
)

const (
	trackBoxScale          = 2
	trackCircleRadius      = 16
	trackCrosshairArm      = 24
	trackingProfileGeneral = "general"
	trackingProfileBall    = "rolling_ball"
)

type TrackingSettings struct {
	Profile               string
	MinArea               float64
	MaxArea               float64
	SlowMinSpeed          float64
	MinSpeed              float64
	MaxMatchDistance      float64
	MinHits               int
	BlurSize              int
	ForegroundThreshold   float64
	PreEventDuration      time.Duration
	PostEventDuration     time.Duration
	RawSegmentDuration    time.Duration
	RawSegmentOverlap     time.Duration
	MOG2History           int
	MOG2VarThreshold      float64
	TrackingROIHeightFrac float64
	GenerateObjectGIFs    bool
}

func DefaultTrackingSettings() TrackingSettings {
	return DefaultTrackingSettingsForProfile(trackingProfileGeneral)
}

func DefaultTrackingSettingsForProfile(profile string) TrackingSettings {
	switch profile {
	case trackingProfileBall:
		return TrackingSettings{
			Profile:               trackingProfileBall,
			MinArea:               20.0,
			MaxArea:               60000.0,
			SlowMinSpeed:          1.5,
			MinSpeed:              4.0,
			MaxMatchDistance:      140.0,
			MinHits:               2,
			BlurSize:              3,
			ForegroundThreshold:   160.0,
			PreEventDuration:      5 * time.Second,
			PostEventDuration:     5 * time.Second,
			RawSegmentDuration:    0,
			RawSegmentOverlap:     1 * time.Second,
			MOG2History:           300,
			MOG2VarThreshold:      12.0,
			TrackingROIHeightFrac: 0.90,
		}
	default:
		return TrackingSettings{
			Profile:               trackingProfileGeneral,
			MinArea:               10.0,
			MaxArea:               15000.0,
			SlowMinSpeed:          14.0,
			MinSpeed:              55.0,
			MaxMatchDistance:      100.0,
			MinHits:               3,
			BlurSize:              5,
			ForegroundThreshold:   220.0,
			PreEventDuration:      5 * time.Second,
			PostEventDuration:     5 * time.Second,
			RawSegmentDuration:    0,
			RawSegmentOverlap:     1 * time.Second,
			MOG2History:           500,
			MOG2VarThreshold:      24.0,
			TrackingROIHeightFrac: 0.90,
		}
	}
}

func normalizeTrackingProfile(profile string) string {
	switch profile {
	case trackingProfileBall:
		return trackingProfileBall
	default:
		return trackingProfileGeneral
	}
}

func trackingProfileLabel(profile string) string {
	switch normalizeTrackingProfile(profile) {
	case trackingProfileBall:
		return "Rolling Ball"
	default:
		return "General"
	}
}

func trackingProfileFromLabel(label string) string {
	switch label {
	case "Rolling Ball":
		return trackingProfileBall
	default:
		return trackingProfileGeneral
	}
}

func trackingProfileOptions() []string {
	return []string{
		trackingProfileLabel(trackingProfileGeneral),
		trackingProfileLabel(trackingProfileBall),
	}
}

func NormalizeTrackingSettings(settings TrackingSettings) TrackingSettings {
	wasZeroValue := settings == (TrackingSettings{})
	settings.Profile = normalizeTrackingProfile(settings.Profile)
	defaults := DefaultTrackingSettingsForProfile(settings.Profile)
	if wasZeroValue {
		return defaults
	}

	if settings.MinArea < 0 || !isFiniteFloat(settings.MinArea) {
		settings.MinArea = defaults.MinArea
	}
	if settings.MaxArea <= settings.MinArea || !isFiniteFloat(settings.MaxArea) {
		settings.MaxArea = defaults.MaxArea
	}
	if settings.SlowMinSpeed < 0 || !isFiniteFloat(settings.SlowMinSpeed) {
		settings.SlowMinSpeed = defaults.SlowMinSpeed
	}
	if settings.MinSpeed < settings.SlowMinSpeed || !isFiniteFloat(settings.MinSpeed) {
		settings.MinSpeed = defaults.MinSpeed
	}
	if settings.MaxMatchDistance <= 0 || !isFiniteFloat(settings.MaxMatchDistance) {
		settings.MaxMatchDistance = defaults.MaxMatchDistance
	}
	if settings.MinHits < 1 {
		settings.MinHits = defaults.MinHits
	}
	if settings.BlurSize < 1 || settings.BlurSize%2 == 0 {
		settings.BlurSize = defaults.BlurSize
	}
	if settings.ForegroundThreshold < 0 || settings.ForegroundThreshold > 255 || !isFiniteFloat(settings.ForegroundThreshold) {
		settings.ForegroundThreshold = defaults.ForegroundThreshold
	}
	if settings.PreEventDuration < 0 {
		settings.PreEventDuration = defaults.PreEventDuration
	}
	if settings.PostEventDuration < 0 {
		settings.PostEventDuration = defaults.PostEventDuration
	}
	if settings.RawSegmentDuration < 0 {
		settings.RawSegmentDuration = defaults.RawSegmentDuration
	}
	if settings.RawSegmentOverlap < 0 {
		settings.RawSegmentOverlap = defaults.RawSegmentOverlap
	}
	if settings.RawSegmentDuration > 0 && settings.RawSegmentOverlap >= settings.RawSegmentDuration {
		settings.RawSegmentOverlap = defaults.RawSegmentOverlap
		if settings.RawSegmentOverlap >= settings.RawSegmentDuration {
			settings.RawSegmentOverlap = settings.RawSegmentDuration / 2
		}
	}
	if settings.MOG2History < 1 {
		settings.MOG2History = defaults.MOG2History
	}
	if settings.MOG2VarThreshold <= 0 || !isFiniteFloat(settings.MOG2VarThreshold) {
		settings.MOG2VarThreshold = defaults.MOG2VarThreshold
	}
	if settings.TrackingROIHeightFrac <= 0 || settings.TrackingROIHeightFrac > 1 || !isFiniteFloat(settings.TrackingROIHeightFrac) {
		settings.TrackingROIHeightFrac = defaults.TrackingROIHeightFrac
	}

	return settings
}

func isFiniteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Overlay colors for the live view and the tracked event exports.
var (
	fastTrackColor = color.RGBA{R: 255, G: 255}
	fastArrowColor = color.RGBA{R: 255, G: 128}
	slowTrackColor = color.RGBA{R: 255}
	slowArrowColor = color.RGBA{R: 255, A: 255}
	textColor      = color.RGBA{G: 255}
	warnColor      = color.RGBA{R: 255, G: 255}
	roiColor       = color.RGBA{G: 255, A: 255}
)

// Detection is one foreground blob extracted from the motion mask for a frame.
type Detection struct {
	Rect   image.Rectangle
	Center image.Point
	Area   float64
}

// Track is the in-memory state for one object candidate as it moves across
// successive frames.
type Track struct {
	ID int

	Rect         image.Rectangle
	Position     image.Point
	PrevPosition image.Point

	VX    float64
	VY    float64
	Speed float64

	LastUpdate time.Time

	Hits   int
	Missed int

	Trail []image.Point
}

// TrackMetadata is the persisted JSON representation of one accepted track for
// a frame.
type TrailPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type TrackMetadata struct {
	ID   int    `json:"id"`
	Type string `json:"type"`

	X int `json:"x"`
	Y int `json:"y"`

	BoxX      int `json:"box_x"`
	BoxY      int `json:"box_y"`
	BoxWidth  int `json:"box_width"`
	BoxHeight int `json:"box_height"`

	VX    float64      `json:"vx"`
	VY    float64      `json:"vy"`
	Speed float64      `json:"speed_px_s"`
	Trail []TrailPoint `json:"trail"`
}

const (
	trackTypeSlow = "slow"
	trackTypeFast = "fast"
)

// FrameMetadata stores the tracking result for one source frame.
type FrameMetadata struct {
	SourceFrame int             `json:"source_frame"`
	TimeUnixNS  int64           `json:"time_unix_ns"`
	TimeMS      int64           `json:"time_ms"`
	MeanLuma    float64         `json:"mean_luma"`
	Tracks      []TrackMetadata `json:"tracks"`
}

// CaptureMetadata describes where and how a DWARF recording was captured.
// Pointer fields distinguish an unknown value from a valid zero value.
type CaptureMetadata struct {
	Source         string   `json:"source,omitempty"`
	Camera         string   `json:"camera,omitempty"`
	Latitude       *float64 `json:"latitude,omitempty"`
	Longitude      *float64 `json:"longitude,omitempty"`
	AltitudeM      *float64 `json:"altitude_m,omitempty"`
	AzimuthDeg     *float64 `json:"azimuth_deg,omitempty"`
	ElevationDeg   *float64 `json:"elevation_deg,omitempty"`
	ExposureMS     *float64 `json:"exposure_ms,omitempty"`
	Gain           *float64 `json:"gain,omitempty"`
	LocationSource string   `json:"location_source,omitempty"`
}

type PhotometricSummary struct {
	MeanLuma float64 `json:"mean_luma"`
	MinLuma  float64 `json:"min_luma"`
	MaxLuma  float64 `json:"max_luma"`
	Samples  int     `json:"samples"`
}

// EventSummary is the short per-event manifest written beside the captured
// videos and full tracking JSON.
type EventSummary struct {
	EventID           string             `json:"event_id"`
	StartedAt         time.Time          `json:"started_at"`
	EndedAt           time.Time          `json:"ended_at"`
	DurationSeconds   float64            `json:"duration_seconds"`
	Width             int                `json:"width"`
	Height            int                `json:"height"`
	FPS               float64            `json:"fps"`
	Frames            int                `json:"frames"`
	UniqueObjects     int                `json:"unique_objects"`
	HighestSpeedPxSec float64            `json:"highest_speed_px_s"`
	OriginalVideo     string             `json:"original_video"`
	TrackedVideo      string             `json:"tracked_video"`
	MaskedVideo       string             `json:"masked_video"`
	TrackCropsDir     string             `json:"track_crops_dir"`
	TrackNamesFile    string             `json:"track_names_file"`
	TrackingMetadata  string             `json:"tracking_metadata"`
	TrackingSettings  TrackingSettings   `json:"tracking_settings"`
	Capture           CaptureMetadata    `json:"capture"`
	Photometry        PhotometricSummary `json:"photometry"`
}

// EventMetadata contains the full timeline for a recorded event.
type EventMetadata struct {
	EventID   string          `json:"event_id"`
	StartedAt time.Time       `json:"started_at"`
	FPS       float64         `json:"fps"`
	Width     int             `json:"width"`
	Height    int             `json:"height"`
	Capture   CaptureMetadata `json:"capture"`
	Frames    []FrameMetadata `json:"frames"`
}

type RawVideoSegment struct {
	Index               int    `json:"index"`
	File                string `json:"file"`
	StartFrame          int    `json:"start_frame"`
	EndFrame            int    `json:"end_frame"`
	StartTimeMS         int64  `json:"start_time_ms"`
	EndTimeMS           int64  `json:"end_time_ms"`
	Frames              int    `json:"frames"`
	OverlapBeforeFrames int    `json:"overlap_before_frames"`
	OverlapAfterFrames  int    `json:"overlap_after_frames"`
}

type RawSegmentManifest struct {
	SessionID       string            `json:"session_id"`
	CreatedAt       time.Time         `json:"created_at"`
	FPS             float64           `json:"fps"`
	Width           int               `json:"width"`
	Height          int               `json:"height"`
	Capture         CaptureMetadata   `json:"capture"`
	SegmentDuration time.Duration     `json:"segment_duration"`
	SegmentOverlap  time.Duration     `json:"segment_overlap"`
	Segments        []RawVideoSegment `json:"segments"`
}

type DwarfQueuedRecording struct {
	Camera          string          `json:"camera"`
	RemotePath      string          `json:"remote_path"`
	RemoteName      string          `json:"remote_name"`
	LocalPath       string          `json:"local_path"`
	RecordingName   string          `json:"recording_name,omitempty"`
	RecordingStart  time.Time       `json:"recording_start,omitempty"`
	DownloadedAt    time.Time       `json:"downloaded_at"`
	DeleteRequested bool            `json:"delete_requested"`
	FailureReason   string          `json:"failure_reason,omitempty"`
	Capture         CaptureMetadata `json:"capture"`
}

// BufferedFrame is one pre-event frame reference. Seekable files keep only
// metadata here; live inputs use temporary image files until an event begins.
type BufferedFrame struct {
	ImagePath string
	MaskPath  string
	Timestamp time.Time
	Metadata  FrameMetadata
}

// EventRecorder owns the output files and the metadata accumulated while one
// event is being written to disk.
type EventRecorder struct {
	RawWriter     *gocv.VideoWriter
	TrackedWriter *gocv.VideoWriter
	MaskedWriter  *gocv.VideoWriter
	TrackingFile  *os.File
	WriteOriginal bool
	WriteMask     bool

	Directory string
	StartedAt time.Time
	FPS       float64
	Width     int
	Height    int

	EventID            string
	SeenIDs            map[int]struct{}
	FramesWritten      int
	TrackingFrameCount int
	TrackingStreamOpen bool

	HighestSpeed float64
	Settings     TrackingSettings
	Capture      CaptureMetadata
	LumaSum      float64
	MinLuma      float64
	MaxLuma      float64
}

// Source mode names used by flags and the optional startup prompt.
const (
	sourceDwarf = "dwarf"
	sourceVideo = "video"
)
