package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jlambert/trackflights/internal/jobs"
)

type trackingOptions struct {
	MinArea   *float64 `json:"minArea"`
	SlowSpeed *float64 `json:"slowSpeed"`
	FastSpeed *float64 `json:"fastSpeed"`
	Threshold *float64 `json:"threshold"`
}
type trackingState struct {
	mu      sync.Mutex
	namesMu sync.Mutex
	videoMu sync.Mutex
	relayMu sync.Mutex
	jobID   string
	preview []byte
	frame   map[string]any
}

func (s *Server) trackingRoot() string { return filepath.Join(s.dataRoot, "tracking-input") }
func (s *Server) trackingEventsRoot() string {
	p := s.currentConfig().Tracking.OutputDir
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.dataRoot, p)
	}
	return p
}
func trackingWorkerPath() string {
	if p := os.Getenv("TRACKFLIGHTS_TRACKING_WORKER"); p != "" {
		return p
	}
	return filepath.Join("bin", "tracking-worker")
}
func (s *Server) trackingStatus(w http.ResponseWriter, r *http.Request) {
	s.tracking.mu.Lock()
	defer s.tracking.mu.Unlock()
	info, err := os.Stat(trackingWorkerPath())
	available := err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0
	writeJSON(w, 200, map[string]any{"workerAvailable": available, "job": s.jobs.Get(s.tracking.jobID), "frame": s.tracking.frame, "hasPreview": len(s.tracking.preview) > 0})
}
func (s *Server) trackingInputs(w http.ResponseWriter, r *http.Request) {
	items := []map[string]string{}
	if info, err := os.Stat(filepath.Join("testdata", "tracking", "tele-sample.mp4")); err == nil && info.Mode().IsRegular() {
		items = append(items, map[string]string{"id": "sample", "name": "DWARF TELE sample"})
	}
	entries, err := os.ReadDir(s.trackingRoot())
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "cannot list videos", 500)
		return
	}
	for _, e := range entries {
		if e.Type().IsRegular() && isVideo(e.Name()) {
			items = append(items, map[string]string{"id": e.Name(), "name": e.Name()})
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

var safeVideoName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func isVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".avi", ".mov", ".mkv":
		return true
	}
	return false
}
func (s *Server) trackingUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		http.Error(w, "invalid or oversized upload", 400)
		return
	}
	defer r.MultipartForm.RemoveAll()
	src, header, err := r.FormFile("video")
	if err != nil || !isVideo(header.Filename) {
		http.Error(w, "select an MP4, AVI, MOV or MKV file", 400)
		return
	}
	defer src.Close()
	if err = os.MkdirAll(s.trackingRoot(), 0750); err != nil {
		http.Error(w, "cannot store video", 500)
		return
	}
	tmp, err := os.CreateTemp(s.trackingRoot(), ".upload-*")
	if err != nil {
		http.Error(w, "cannot store video", 500)
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, src)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		http.Error(w, "upload failed", 500)
		return
	}
	base := filepath.Base(strings.ReplaceAll(header.Filename, `\`, "/"))
	base = safeVideoName.ReplaceAllString(base, "_")
	if len(base) > 100 {
		base = base[:100] + strings.ToLower(filepath.Ext(header.Filename))
	}
	name := strings.TrimPrefix(filepath.Base(tmp.Name()), ".") + "-" + base
	if err = os.Rename(tmp.Name(), filepath.Join(s.trackingRoot(), name)); err != nil {
		http.Error(w, "cannot store video", 500)
		return
	}
	writeJSON(w, 201, map[string]string{"id": name, "name": name})
}
func (s *Server) trackingStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode      string `json:"mode"`
		Input     string `json:"input"`
		Profile   string `json:"profile"`
		Original  *bool  `json:"original"`
		Mask      bool   `json:"mask"`
		FullVideo bool   `json:"fullVideo"`
		trackingOptions
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if req.Profile != "general" && req.Profile != "rolling_ball" {
		http.Error(w, "invalid profile", 400)
		return
	}
	maxArea, slow, fast := 15000.0, 14.0, 55.0
	if req.Profile == "rolling_ball" {
		maxArea, slow, fast = 60000, 1.5, 4
	}
	if req.MinArea != nil && (*req.MinArea < 0 || *req.MinArea >= maxArea) {
		http.Error(w, "minimum area is outside the profile range", 400)
		return
	}
	if req.SlowSpeed != nil {
		slow = *req.SlowSpeed
	}
	if req.FastSpeed != nil {
		fast = *req.FastSpeed
	}
	if slow < 0 || fast < slow || fast > 100000 {
		http.Error(w, "track speeds must be non-negative and fast speed must be at least slow speed", 400)
		return
	}
	if req.Threshold != nil && (*req.Threshold < 0 || *req.Threshold > 255) {
		http.Error(w, "foreground threshold must be between 0 and 255", 400)
		return
	}
	input := req.Input
	switch req.Mode {
	case "file":
		if input == "sample" {
			input = filepath.Join("testdata", "tracking", "tele-sample.mp4")
		} else {
			if filepath.Base(input) != input || !isVideo(input) {
				http.Error(w, "invalid video", 400)
				return
			}
			input = filepath.Join(s.trackingRoot(), input)
		}
		info, err := os.Lstat(input)
		if err != nil || !info.Mode().IsRegular() {
			http.Error(w, "video not found", 404)
			return
		}
	case "live":
		if req.FullVideo {
			http.Error(w, "full video export requires a recorded video", 400)
			return
		}
		u, err := url.Parse(input)
		if err != nil || u.Scheme != "rtsp" || u.Hostname() == "" {
			http.Error(w, "enter a valid RTSP URL", 400)
			return
		}
	default:
		http.Error(w, "invalid input mode", 400)
		return
	}
	worker := trackingWorkerPath()
	info, err := os.Stat(worker)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		http.Error(w, "tracking worker unavailable; build it with make build-tracking", 503)
		return
	}
	worker, err = filepath.Abs(worker)
	if err != nil {
		http.Error(w, "invalid worker path", 500)
		return
	}
	if err = os.MkdirAll(s.trackingEventsRoot(), 0750); err != nil {
		http.Error(w, "cannot create events directory", 500)
		return
	}
	s.tracking.mu.Lock()
	defer s.tracking.mu.Unlock()
	existing := s.jobs.Get(s.tracking.jobID)
	if existing.State == jobs.Queued || existing.State == jobs.Running {
		http.Error(w, "tracking already running", 409)
		return
	}
	s.tracking.preview = nil
	s.tracking.frame = nil
	original := true
	if req.Original != nil {
		original = *req.Original
	}
	job := s.jobs.Start("object-tracking", func(ctx context.Context, progress func(float64, string)) error {
		return s.runTracking(ctx, worker, req.Mode, input, req.Profile, original, req.Mask, req.FullVideo, req.trackingOptions, progress)
	})
	s.tracking.jobID = job.ID
	writeJSON(w, 202, job)
}
func (s *Server) runTracking(ctx context.Context, worker, mode, input, profile string, original, mask, fullVideo bool, options trackingOptions, progress func(float64, string)) error {
	args := []string{"-input", input, "-mode", mode, "-profile", profile, "-original=" + strconv.FormatBool(original), "-mask=" + strconv.FormatBool(mask), "-full-video=" + strconv.FormatBool(fullVideo), "-output", s.trackingEventsRoot()}
	for _, option := range []struct {
		name  string
		value *float64
	}{{"-min-area", options.MinArea}, {"-slow-speed", options.SlowSpeed}, {"-fast-speed", options.FastSpeed}, {"-threshold", options.Threshold}} {
		if option.value != nil {
			args = append(args, option.name, strconv.FormatFloat(*option.value, 'g', -1, 64))
		}
	}
	cmd := exec.Command(worker, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: 16000}
	if err = cmd.Start(); err != nil {
		return err
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-finished:
			case <-time.After(8 * time.Second):
				_ = cmd.Process.Kill()
			}
		case <-finished:
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var eventIDs []string
	sessionID := ""
	for scanner.Scan() {
		var data map[string]any
		if json.Unmarshal(scanner.Bytes(), &data) != nil {
			continue
		}
		if data["type"] == "frame" {
			if encoded, ok := data["jpeg"].(string); ok {
				preview, err := base64.StdEncoding.DecodeString(encoded)
				if err == nil && len(preview) <= 2<<20 {
					s.tracking.mu.Lock()
					s.tracking.preview = preview
					s.tracking.mu.Unlock()
				}
				delete(data, "jpeg")
			}
			s.tracking.mu.Lock()
			s.tracking.frame = data
			s.tracking.mu.Unlock()
			p, _ := data["progress"].(float64)
			message, _ := data["status"].(string)
			progress(p, message)
		}
		if data["type"] == "export" {
			progress(0.99, "Encoding full tracked video")
		}
		if data["type"] == "event" || data["type"] == "session" {
			if dir, ok := data["directory"].(string); ok {
				id := filepath.Base(dir)
				if id != "." && id != ".." && filepath.Clean(dir) == filepath.Clean(filepath.Join(s.trackingEventsRoot(), id)) {
					if data["type"] == "event" {
						eventIDs = append(eventIDs, id)
					} else {
						sessionID = id
					}
				}
			}
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	close(finished)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanErr != nil {
		return fmt.Errorf("read tracking updates: %w", scanErr)
	}
	if waitErr != nil {
		return fmt.Errorf("tracking failed: %s", strings.TrimSpace(stderr.String()))
	}
	settings := s.currentConfig().Tracking
	if settings.NostrEnabled != nil && *settings.NostrEnabled {
		progress(0.99, "Publishing tracking results to Nostr")
		if err := s.publishTrackingResults(ctx, settings, input, mode, eventIDs, sessionID); err != nil {
			if errors.Is(err, errTrackingMediaUnavailable) {
				progress(1, "Results published, but some object media could not be uploaded to Blossom")
				return nil
			}
			return fmt.Errorf("tracking completed, but Nostr publication failed: %w", err)
		}
		progress(1, "Tracking results published to Nostr")
	}
	return nil
}

type limitedWriter struct {
	w         io.Writer
	remaining int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if l.remaining > 0 {
		b := p
		if len(b) > l.remaining {
			b = b[:l.remaining]
		}
		_, _ = l.w.Write(b)
		l.remaining -= len(b)
	}
	return n, nil
}
func (s *Server) trackingPreview(w http.ResponseWriter, r *http.Request) {
	s.tracking.mu.Lock()
	data := append([]byte(nil), s.tracking.preview...)
	s.tracking.mu.Unlock()
	if len(data) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

const sampleEventID = "sample-event"

func (s *Server) trackingEventRoot(id string) (string, string) {
	if id == sampleEventID {
		return filepath.Join("testdata", "tracking"), sampleEventID
	}
	return s.trackingEventsRoot(), id
}
func (s *Server) trackingEvents(w http.ResponseWriter, r *http.Request) {
	sampleItems := []map[string]any{}
	if data, err := os.ReadFile(filepath.Join("testdata", "tracking", sampleEventID, "event.json")); err == nil && len(data) < 1<<20 {
		var summary any
		if json.Unmarshal(data, &summary) == nil {
			sampleItems = append(sampleItems, map[string]any{"id": sampleEventID, "summary": summary})
		}
	}
	root, err := os.OpenRoot(s.trackingEventsRoot())
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, 200, map[string]any{"items": sampleItems})
			return
		}
		http.Error(w, "cannot list events", 500)
		return
	}
	defer root.Close()
	dirs, err := os.ReadDir(s.trackingEventsRoot())
	if err != nil {
		http.Error(w, "cannot list events", 500)
		return
	}
	items := sampleItems
	for _, entry := range dirs {
		if entry.Name() == sampleEventID {
			continue
		}
		if !entry.IsDir() {
			continue
		}
		f, err := root.Open(filepath.Join(entry.Name(), "event.json"))
		if err != nil {
			continue
		}
		var summary any
		err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&summary)
		f.Close()
		if err == nil {
			items = append(items, map[string]any{"id": entry.Name(), "summary": summary})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["id"].(string) > items[j]["id"].(string) })
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) trackingEventFiles(w http.ResponseWriter, r *http.Request) {
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
	items := []string{}
	priority := map[string]bool{}
	for _, name := range []string{"event.json", "tracking.json", "tracked.mp4", "tracked.avi", "track_names.json"} {
		f, err := root.Open(filepath.Join(relativeID, name))
		if err != nil {
			continue
		}
		info, err := f.Stat()
		f.Close()
		if err == nil && info.Mode().IsRegular() {
			items = append(items, name)
			priority[name] = true
		}
	}
	err = fs.WalkDir(root.FS(), relativeID, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Type().IsRegular() {
			name := strings.TrimPrefix(path, relativeID+"/")
			if priority[name] {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".json", ".jpg", ".png", ".gif", ".avi", ".mp4":
				if len(items) < 5000 {
					items = append(items, name)
				}
			}
		}
		return nil
	})
	if err != nil {
		http.NotFound(w, r)
		return
	}
	original, _, _, openErr := s.openTrackingOriginal(id)
	if openErr == nil {
		original.Close()
	}
	writeJSON(w, 200, map[string]any{"items": items, "originalAvailable": openErr == nil})
}

var objectCropName = regexp.MustCompile(`^frame_[0-9]+_[0-9]+ms\.jpe?g$`)

func (s *Server) trackingObjectCrops(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	track, err := strconv.Atoi(r.PathValue("track"))
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || err != nil || track < 1 || track > 1000000000 {
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
	dir := filepath.Join(relativeID, "track_crops", fmt.Sprintf("object_%04d", track))
	f, err := root.Open(dir)
	if os.IsNotExist(err) {
		writeJSON(w, 200, map[string]any{"items": []string{}})
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		http.Error(w, "cannot list object crops", 500)
		return
	}
	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && objectCropName.MatchString(entry.Name()) {
			items = append(items, filepath.ToSlash(filepath.Join("track_crops", fmt.Sprintf("object_%04d", track), entry.Name())))
		}
	}
	sort.Strings(items)
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) trackingEventFile(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("path")
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || !filepath.IsLocal(name) {
		http.NotFound(w, r)
		return
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json", ".jpg", ".png", ".gif", ".avi", ".mp4":
	default:
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
	f, err := root.Open(filepath.Join(relativeID, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
