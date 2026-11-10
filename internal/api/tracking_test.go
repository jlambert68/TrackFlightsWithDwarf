package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrackingVideoAndWorker(t *testing.T) {
	s := newTestServer(t)
	worker := filepath.Join(t.TempDir(), "worker")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"frame\",\"frame\":42,\"progress\":0.5,\"status\":\"tracking\",\"jpeg\":\"/9j/2Q==\"}'\n"
	if err := os.WriteFile(worker, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRACKFLIGHTS_TRACKING_WORKER", worker)
	var data bytes.Buffer
	form := multipart.NewWriter(&data)
	part, err := form.CreateFormFile("video", "test.mp4")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("fixture"))
	form.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tracking/inputs", &data)
	request.Header.Set("Content-Type", form.FormDataContentType())
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, request)
	if res.Code != 201 {
		t.Fatal(res.Code, res.Body.String())
	}
	var upload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/tracking/start", strings.NewReader(`{"mode":"file","input":"`+upload.ID+`","profile":"general"}`)))
	if res.Code != 202 {
		t.Fatal(res.Code, res.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	json.Unmarshal(res.Body.Bytes(), &job)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.jobs.Get(job.ID).State != "succeeded" {
		time.Sleep(10 * time.Millisecond)
	}
	if j := s.jobs.Get(job.ID); j.State != "succeeded" {
		t.Fatalf("tracking: %+v", j)
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/preview", nil))
	if res.Code != 200 || !bytes.Equal(res.Body.Bytes(), []byte{0xff, 0xd8, 0xff, 0xd9}) {
		t.Fatalf("preview %d %x", res.Code, res.Body.Bytes())
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/tracking/start", strings.NewReader(`{"mode":"file","input":"`+upload.ID+`","profile":"general","threshold":256}`)))
	if res.Code != 400 {
		t.Fatalf("invalid threshold status %d", res.Code)
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/tracking/start", strings.NewReader(`{"mode":"file","input":"../secret.mp4","profile":"general"}`)))
	if res.Code != 400 {
		t.Fatalf("traversal status %d", res.Code)
	}
}
func TestTrackingEventFilesAreConfined(t *testing.T) {
	s := newTestServer(t)
	root := s.trackingEventsRoot()
	if err := os.MkdirAll(filepath.Join(root, "event-one"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "event-one", "event.json"), []byte(`{"count":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.json")
	os.WriteFile(outside, []byte(`{"secret":true}`), 0644)
	os.Symlink(outside, filepath.Join(root, "event-one", "escape.json"))
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events", nil))
	if res.Code != 200 || !strings.Contains(res.Body.String(), "event-one") {
		t.Fatal(res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/files/escape.json", nil))
	if res.Code != 404 {
		t.Fatalf("symlink escaped root: %d", res.Code)
	}
}

func TestTrackingOpenCVIntegration(t *testing.T) {
	worker := os.Getenv("TRACKFLIGHTS_INTEGRATION_WORKER")
	if worker == "" {
		t.Skip("set TRACKFLIGHTS_INTEGRATION_WORKER to run the OpenCV integration test")
	}
	s := newTestServer(t)
	t.Setenv("TRACKFLIGHTS_TRACKING_WORKER", worker)
	video, err := os.ReadFile(filepath.Join("..", "..", "testdata", "tracking", "tele-sample.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	form := multipart.NewWriter(&data)
	part, err := form.CreateFormFile("video", "sample.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(video); err != nil {
		t.Fatal(err)
	}
	form.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tracking/inputs", &data)
	request.Header.Set("Content-Type", form.FormDataContentType())
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, request)
	if res.Code != 201 {
		t.Fatal(res.Code, res.Body.String())
	}
	var upload struct {
		ID string `json:"id"`
	}
	json.Unmarshal(res.Body.Bytes(), &upload)
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/tracking/start", strings.NewReader(`{"mode":"file","input":"`+upload.ID+`","profile":"general","minArea":6,"slowSpeed":10,"fastSpeed":40,"threshold":200}`)))
	if res.Code != 202 {
		t.Fatal(res.Code, res.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	json.Unmarshal(res.Body.Bytes(), &job)
	deadline := time.Now().Add(20 * time.Second)
	var preview bool
	for time.Now().Before(deadline) {
		s.tracking.mu.Lock()
		preview = len(s.tracking.preview) > 0
		s.tracking.mu.Unlock()
		if preview {
			break
		}
		if j := s.jobs.Get(job.ID); j.State == "failed" {
			t.Fatal(j.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.jobs.Cancel(job.ID)
	deadline = time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		state := s.jobs.Get(job.ID).State
		if state == "cancelled" || state == "succeeded" || state == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !preview {
		t.Fatal("tracking worker did not produce a preview")
	}
	if state := s.jobs.Get(job.ID).State; state != "cancelled" && state != "succeeded" {
		t.Fatalf("worker did not stop: %s", state)
	}
}
