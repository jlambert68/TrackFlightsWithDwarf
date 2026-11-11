package api

import (
	"bytes"
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
