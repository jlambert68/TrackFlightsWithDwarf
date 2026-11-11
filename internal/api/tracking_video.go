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
)

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
	s.serveTrackingVideo(w, r, input, rootPath+"/"+relativeID+"/tracked.avi", 0)
}

// openTrackingOriginal only opens an event's saved original or an uploaded
// session input. It never accepts a path supplied by the HTTP client.
func (s *Server) openTrackingOriginal(id string) (*os.File, string, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return nil, "", os.ErrNotExist
	}
	rootPath, relativeID := s.trackingEventRoot(id)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	for _, name := range []string{"original.mp4", "original.avi"} {
		f, err := root.Open(filepath.Join(relativeID, name))
		if err == nil {
			if info, e := f.Stat(); e == nil && info.Mode().IsRegular() {
				return f, filepath.Join(rootPath, relativeID, name), nil
			}
			f.Close()
		}
	}
	manifest, err := root.Open(filepath.Join(relativeID, "event.json"))
	if err != nil {
		return nil, "", err
	}
	var summary struct {
		Type   string `json:"type"`
		Source string `json:"source"`
	}
	err = json.NewDecoder(io.LimitReader(manifest, 1<<20)).Decode(&summary)
	manifest.Close()
	if err != nil || summary.Type != "tracking_session" || summary.Source == "" || filepath.Base(summary.Source) != summary.Source || !isVideo(summary.Source) {
		return nil, "", os.ErrNotExist
	}
	inputDir := s.trackingRoot()
	if summary.Source == "tele-sample.mp4" {
		inputDir = filepath.Join("testdata", "tracking")
	}
	inputs, err := os.OpenRoot(inputDir)
	if err != nil {
		return nil, "", err
	}
	defer inputs.Close()
	f, err := inputs.Open(summary.Source)
	if err != nil {
		return nil, "", err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, "", os.ErrNotExist
	}
	return f, filepath.Join(inputDir, summary.Source), nil
}

func (s *Server) trackingOriginalVideo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	input, sourceKey, err := s.openTrackingOriginal(id)
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
	s.serveTrackingVideo(w, r, input, sourceKey, fps)
}

func (s *Server) serveTrackingVideo(w http.ResponseWriter, r *http.Request, input *os.File, sourceKey string, fps float64) {
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%.12g", sourceKey, info.Size(), info.ModTime().UnixNano(), fps)))
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
			args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", "/proc/self/fd/3", "-an"}
			if fps > 0 {
				filter = fmt.Sprintf("setpts=N/(%.12g*TB),%s", fps, filter)
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
