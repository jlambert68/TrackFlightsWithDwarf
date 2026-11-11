package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type videoSegment struct{ start, frames int }

// trackingEventVideo converts older MJPEG/AVI event recordings once, without
// changing the original, then serves the MP4 with range support for seeking.
func (s *Server) trackingEventVideo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		http.NotFound(w, r)
		return
	}
	rootPath, relativeID := s.trackingEventRoot(id)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	input, err := root.Open(filepath.Join(relativeID, "tracked.avi"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer input.Close()
	s.serveTrackingVideo(w, r, input, rootPath+"/"+relativeID+"/tracked.avi", 0, videoSegment{})
}

// openTrackingOriginal only opens an event's saved original or an uploaded
// session input. It never accepts a path supplied by the HTTP client.
func (s *Server) openTrackingOriginal(id string) (*os.File, string, videoSegment, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return nil, "", videoSegment{}, os.ErrNotExist
	}
	rootPath, relativeID := s.trackingEventRoot(id)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", videoSegment{}, err
	}
	defer root.Close()
	for _, name := range []string{"original.mp4", "original.avi"} {
		f, err := root.Open(filepath.Join(relativeID, name))
		if err == nil {
			if info, e := f.Stat(); e == nil && info.Mode().IsRegular() {
				return f, filepath.Join(rootPath, relativeID, name), videoSegment{}, nil
			}
			f.Close()
		}
	}
	manifest, err := root.Open(filepath.Join(relativeID, "event.json"))
	if err != nil {
		return nil, "", videoSegment{}, err
	}
	var summary struct {
		Type   string `json:"type"`
		Source string `json:"source"`
	}
	err = json.NewDecoder(io.LimitReader(manifest, 1<<20)).Decode(&summary)
	manifest.Close()
	if err != nil {
		return nil, "", videoSegment{}, err
	}
	if summary.Type != "tracking_session" {
		return s.recoverEventOriginal(root, rootPath, relativeID)
	}
	f, path, err := s.openTrackingSessionSource(summary.Source)
	return f, path, videoSegment{}, err
}

func (s *Server) openTrackingSessionSource(source string) (*os.File, string, error) {
	if source == "" || filepath.Base(source) != source || !isVideo(source) {
		return nil, "", os.ErrNotExist
	}
	inputDir := s.trackingRoot()
	if source == "tele-sample.mp4" {
		inputDir = filepath.Join("testdata", "tracking")
	}
	inputs, err := os.OpenRoot(inputDir)
	if err != nil {
		return nil, "", err
	}
	defer inputs.Close()
	f, err := inputs.Open(source)
	if err != nil {
		return nil, "", err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, "", os.ErrNotExist
	}
	return f, filepath.Join(inputDir, source), nil
}

type trackingTimeline struct {
	fps        float64
	firstFrame int
	firstTime  int64
	frames     int
}

// Read the first timestamp and, for an event, verify its saved source frame
// numbers are contiguous. Metadata is streamed rather than loaded in memory.
func readTrackingTimeline(root *os.Root, id string, firstOnly bool) (trackingTimeline, error) {
	var result trackingTimeline
	f, err := root.Open(filepath.Join(id, "tracking.json"))
	if err != nil {
		return result, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return result, fmt.Errorf("invalid tracking metadata")
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return result, err
		}
		switch key {
		case "fps":
			if err := dec.Decode(&result.fps); err != nil {
				return result, err
			}
		case "frames":
			token, err := dec.Token()
			if err != nil || token != json.Delim('[') {
				return result, fmt.Errorf("invalid frames")
			}
			for dec.More() {
				var frame struct {
					SourceFrame int   `json:"source_frame"`
					TimeUnixNS  int64 `json:"time_unix_ns"`
				}
				if err := dec.Decode(&frame); err != nil {
					return result, err
				}
				if result.frames == 0 {
					result.firstFrame, result.firstTime = frame.SourceFrame, frame.TimeUnixNS
				}
				if frame.SourceFrame < 1 || frame.SourceFrame > 1_000_000_000 {
					return result, fmt.Errorf("invalid source frame")
				}
				if frame.SourceFrame != result.firstFrame+result.frames {
					return result, fmt.Errorf("source frames are not contiguous")
				}
				result.frames++
				if firstOnly {
					break
				}
			}
			if result.frames == 0 || result.firstFrame < 1 || result.firstTime <= 0 || result.fps <= 0 || result.fps > 240 || math.IsNaN(result.fps) || math.IsInf(result.fps, 0) {
				return result, fmt.Errorf("missing timeline information")
			}
			return result, nil
		default:
			var ignored json.RawMessage
			if err := dec.Decode(&ignored); err != nil {
				return result, err
			}
		}
	}
	return result, fmt.Errorf("tracking frames not found")
}

// Older event clips have no source reference. Recover one only when exactly
// one full session's first-frame timestamp and frame rate match the event.
func (s *Server) recoverEventOriginal(root *os.Root, rootPath, id string) (*os.File, string, videoSegment, error) {
	event, err := readTrackingTimeline(root, id, false)
	if err != nil {
		return nil, "", videoSegment{}, os.ErrNotExist
	}
	f, err := root.Open(filepath.Join(id, "event.json"))
	if err != nil {
		return nil, "", videoSegment{}, err
	}
	var summary struct {
		Frames int `json:"frames"`
	}
	err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&summary)
	f.Close()
	if err != nil || summary.Frames != event.frames {
		return nil, "", videoSegment{}, os.ErrNotExist
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return nil, "", videoSegment{}, err
	}
	matchedSource := ""
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "session-") {
			continue
		}
		id := entry.Name()
		timeline, err := readTrackingTimeline(root, id, true)
		if err != nil || timeline.firstFrame != 1 || math.Abs(timeline.fps-event.fps) > 0.000001 {
			continue
		}
		f, err := root.Open(filepath.Join(id, "event.json"))
		if err != nil {
			continue
		}
		var session struct {
			Type   string `json:"type"`
			Source string `json:"source"`
			Frames int    `json:"frames"`
		}
		err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&session)
		f.Close()
		if err != nil || session.Type != "tracking_session" || session.Frames < event.firstFrame-1+event.frames {
			continue
		}
		offset := time.Duration(float64(event.firstFrame-1) * float64(time.Second) / event.fps)
		if delta := event.firstTime - (timeline.firstTime + int64(offset)); delta < -int64(time.Millisecond) || delta > int64(time.Millisecond) {
			continue
		}
		source, _, err := s.openTrackingSessionSource(session.Source)
		if err != nil {
			continue
		}
		source.Close()
		if matchedSource != "" {
			return nil, "", videoSegment{}, fmt.Errorf("multiple matching source sessions")
		}
		matchedSource = session.Source
	}
	if matchedSource == "" {
		return nil, "", videoSegment{}, os.ErrNotExist
	}
	source, path, err := s.openTrackingSessionSource(matchedSource)
	if err != nil {
		return nil, "", videoSegment{}, err
	}
	return source, path, videoSegment{start: event.firstFrame - 1, frames: event.frames}, nil
}

func (s *Server) trackingOriginalVideo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	input, sourceKey, segment, err := s.openTrackingOriginal(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer input.Close()
	// Use the recorded tracking FPS for frame-accurate playback even when an
	// uploaded source has irregular timestamps.
	fps := 0.0
	rootPath, relativeID := s.trackingEventRoot(id)
	if root, err := os.OpenRoot(rootPath); err == nil {
		if f, err := root.Open(filepath.Join(relativeID, "tracking.json")); err == nil {
			var header struct {
				FPS float64 `json:"fps"`
			}
			// Decode only the header: tracking.json can contain a very large frames array.
			dec := json.NewDecoder(f)
			if token, err := dec.Token(); err == nil && token == json.Delim('{') {
				for dec.More() {
					key, err := dec.Token()
					if err != nil || key == "frames" {
						break
					}
					if key == "fps" {
						if dec.Decode(&header.FPS) != nil {
							break
						}
					} else {
						var value json.RawMessage
						if dec.Decode(&value) != nil {
							break
						}
					}
				}
			}
			if header.FPS > 0 && header.FPS <= 240 && !math.IsNaN(header.FPS) && !math.IsInf(header.FPS, 0) {
				fps = header.FPS
			}
			f.Close()
		}
		root.Close()
	}
	s.serveTrackingVideo(w, r, input, sourceKey, fps, segment)
}

func (s *Server) serveTrackingVideo(w http.ResponseWriter, r *http.Request, input *os.File, sourceKey string, fps float64, segment videoSegment) {
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	cacheKey := fmt.Sprintf("%s\x00%d\x00%d\x00%.12g", sourceKey, info.Size(), info.ModTime().UnixNano(), fps)
	if segment.frames > 0 {
		cacheKey += fmt.Sprintf("\x00%d\x00%d", segment.start, segment.frames)
	}
	key := sha256.Sum256([]byte(cacheKey))
	cacheDir := filepath.Join(s.dataRoot, "tracking-playback")
	if err := os.MkdirAll(cacheDir, 0750); err != nil {
		http.Error(w, "cannot prepare video storage", 500)
		return
	}
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%x.mp4", key))

	ready := func() bool {
		s.tracking.videoMu.Lock()
		defer s.tracking.videoMu.Unlock()
		if r.Context().Err() != nil {
			return false
		}
		if _, err := os.Stat(cachePath); os.IsNotExist(err) {
			if _, err := exec.LookPath("ffmpeg"); err != nil {
				http.Error(w, "FFmpeg is required to play AVI recordings", http.StatusServiceUnavailable)
				return false
			}
			temp, err := os.CreateTemp(cacheDir, ".video-*.mp4")
			if err != nil {
				http.Error(w, "cannot prepare video", 500)
				return false
			}
			tempPath := temp.Name()
			if err := temp.Close(); err != nil {
				os.Remove(tempPath)
				http.Error(w, "cannot prepare video", 500)
				return false
			}
			defer os.Remove(tempPath)
			filter := "pad=ceil(iw/2)*2:ceil(ih/2)*2"
			if segment.frames > 0 {
				filter = fmt.Sprintf("trim=start_frame=%d:end_frame=%d,%s", segment.start, segment.start+segment.frames, filter)
			}
			args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", "/proc/self/fd/3", "-an"}
			if fps > 0 {
				if segment.frames > 0 {
					filter = fmt.Sprintf("trim=start_frame=%d:end_frame=%d,setpts=N/(%.12g*TB),pad=ceil(iw/2)*2:ceil(ih/2)*2", segment.start, segment.start+segment.frames, fps)
				} else {
					filter = fmt.Sprintf("setpts=N/(%.12g*TB),%s", fps, filter)
				}
				args = append(args, "-r", strconv.FormatFloat(fps, 'g', 12, 64))
			}
			args = append(args, "-vf", filter, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", tempPath)
			cmd := exec.CommandContext(r.Context(), "ffmpeg", args...)
			cmd.ExtraFiles = []*os.File{input}
			if err := cmd.Run(); err != nil {
				if r.Context().Err() == nil {
					http.Error(w, "could not convert tracked video; check FFmpeg installation", 500)
				}
				return false
			}
			if err := os.Rename(tempPath, cachePath); err != nil {
				http.Error(w, "cannot save converted video", 500)
				return false
			}
		} else if err != nil {
			http.Error(w, "cannot access converted video", 500)
			return false
		}
		return true
	}()
	if !ready {
		return
	}
	video, err := os.Open(cachePath)
	if err != nil {
		http.Error(w, "cannot open converted video", 500)
		return
	}
	defer video.Close()
	stat, err := video.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		http.Error(w, "cannot open converted video", 500)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "tracked.mp4", stat.ModTime(), video)
}
