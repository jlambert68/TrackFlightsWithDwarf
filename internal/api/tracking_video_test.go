package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrackingAVIPlaybackAndDownload(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	s := newTestServer(t)
	dir := filepath.Join(s.trackingEventsRoot(), "event-one")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	avi := filepath.Join(dir, "tracked.avi")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=128x96:rate=10", "-frames:v", "10", "-c:v", "mjpeg", "-threads", "1", avi)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make AVI: %v: %s", err, out)
	}
	before, err := os.ReadFile(avi)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/video", nil)
	request.Header.Set("Range", "bytes=0-31")
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, request)
	if res.Code != http.StatusPartialContent || res.Body.Len() != 32 || res.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("playback: %d %s %q", res.Code, res.Header().Get("Content-Type"), res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "ftyp") {
		t.Fatalf("not an MP4: %q", res.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(s.dataRoot, "tracking-playback"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("cached videos: %v %v", entries, err)
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/video", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("cached playback: %d %s", res.Code, res.Body.String())
	}
	entries, err = os.ReadDir(filepath.Join(s.dataRoot, "tracking-playback"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected cache contents: %v %v", entries, err)
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/files/tracked.avi", nil))
	if res.Code != http.StatusOK || string(res.Body.Bytes()) != string(before) {
		t.Fatal("original AVI was changed or cannot be downloaded")
	}
}

func TestRecoverEventOriginalFromMatchingSession(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	s := newTestServer(t)
	if err := os.MkdirAll(s.trackingRoot(), 0750); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(s.trackingRoot(), "recording.mp4")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "nullsrc=s=64x64:r=10,geq=lum='N*20+16':cb=128:cr=128", "-frames:v", "10", "-c:v", "mpeg4", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create source: %v: %s", err, out)
	}
	eventDir := filepath.Join(s.trackingEventsRoot(), "event-one")
	sessionDir := filepath.Join(s.trackingEventsRoot(), "session-one")
	for _, dir := range []string{eventDir, sessionDir} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(sessionDir, "event.json"), `{"type":"tracking_session","source":"recording.mp4","frames":10}`)
	write(filepath.Join(sessionDir, "tracking.json"), `{"fps":10,"frames":[{"source_frame":1,"time_unix_ns":1700000000000000000}]}`)
	write(filepath.Join(eventDir, "event.json"), `{"frames":4}`)
	write(filepath.Join(eventDir, "tracking.json"), `{"fps":10,"frames":[{"source_frame":4,"time_unix_ns":1700000000300000000},{"source_frame":5,"time_unix_ns":1700000000400000000},{"source_frame":6,"time_unix_ns":1700000000500000000},{"source_frame":7,"time_unix_ns":1700000000600000000}]}`)
	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/files", nil))
	if !strings.Contains(list.Body.String(), `"originalAvailable":true`) {
		t.Fatalf("source not recovered: %s", list.Body.String())
	}
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/original-video", nil))
	if res.Code != 200 {
		t.Fatalf("original: %d %s", res.Code, res.Body.String())
	}
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(clip, res.Body.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-count_frames", "-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", clip)
	if out, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "4" {
		t.Fatalf("wrong clip frame count: %s %v", out, err)
	}
	frame := func(path string, index int) []byte {
		t.Helper()
		out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-i", path, "-vf", fmt.Sprintf(`select=eq(n\,%d)`, index), "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1").Output()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	want, got := frame(input, 3), frame(clip, 0)
	if len(want) == 0 || len(want) != len(got) {
		t.Fatal("could not decode frames")
	}
	var difference int
	for i := range want {
		delta := int(want[i]) - int(got[i])
		if delta < 0 {
			delta = -delta
		}
		difference += delta
	}
	if difference/len(want) > 8 {
		t.Fatalf("clip starts at the wrong frame: average difference %d", difference/len(want))
	}
	eventTracking := filepath.Join(eventDir, "tracking.json")
	validTracking, err := os.ReadFile(eventTracking)
	if err != nil {
		t.Fatal(err)
	}
	write(eventTracking, `{"fps":10,"frames":[{"source_frame":4,"time_unix_ns":1700000000300000000},{"source_frame":6,"time_unix_ns":1700000000500000000}]}`)
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/original-video", nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("noncontiguous frames were accepted: %d", res.Code)
	}
	if err := os.WriteFile(eventTracking, validTracking, 0640); err != nil {
		t.Fatal(err)
	}
	duplicate := filepath.Join(s.trackingEventsRoot(), "session-two")
	if err := os.MkdirAll(duplicate, 0750); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(duplicate, "event.json"), `{"type":"tracking_session","source":"recording.mp4","frames":10}`)
	write(filepath.Join(duplicate, "tracking.json"), `{"fps":10,"frames":[{"source_frame":1,"time_unix_ns":1700000000000000000}]}`)
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/original-video", nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("ambiguous session was guessed: %d", res.Code)
	}
}

func TestTrackingOriginalVideoPlayback(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	s := newTestServer(t)
	root := s.trackingEventsRoot()
	dir := filepath.Join(root, "event-original")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(`{}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracking.json"), []byte(`{"fps":10,"frames":[]}`), 0640); err != nil {
		t.Fatal(err)
	}
	avi := filepath.Join(dir, "original.avi")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=128x96:rate=10", "-frames:v", "10", "-c:v", "mjpeg", "-threads", "1", avi)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make original AVI: %v: %s", err, out)
	}
	before, err := os.ReadFile(avi)
	if err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-original/files", nil))
	if !strings.Contains(list.Body.String(), `"originalAvailable":true`) {
		t.Fatalf("original unavailable: %s", list.Body.String())
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-original/original-video", nil)
	req.Header.Set("Range", "bytes=0-31")
	s.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusPartialContent || !bytes.Contains(res.Body.Bytes(), []byte("ftyp")) {
		t.Fatalf("original playback: %d %s", res.Code, res.Body.String())
	}
	after, _ := os.ReadFile(avi)
	if !bytes.Equal(before, after) {
		t.Fatal("original was modified")
	}

	if err := os.MkdirAll(s.trackingRoot(), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.trackingRoot(), "input.avi"), before, 0640); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "session-one")
	if err := os.MkdirAll(session, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, "event.json"), []byte(`{"type":"tracking_session","source":"input.avi"}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, "tracking.json"), []byte(`{"fps":10,"frames":[]}`), 0640); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/session-one/original-video", nil))
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes()[:min(res.Body.Len(), 64)], []byte("ftyp")) {
		t.Fatalf("session source playback: %d", res.Code)
	}
	if err := os.WriteFile(filepath.Join(session, "event.json"), []byte(`{"type":"tracking_session","source":"../input.avi"}`), 0640); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/session-one/original-video", nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("unsafe source allowed: %d", res.Code)
	}
}
