package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrackingVideoAndWorker(t *testing.T) {
	s := newTestServer(t)
	worker := filepath.Join(t.TempDir(), "worker")
	argsFile := filepath.Join(t.TempDir(), "worker-args")
	t.Setenv("TRACKFLIGHTS_TEST_ARGS_FILE", argsFile)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TRACKFLIGHTS_TEST_ARGS_FILE\"\nprintf '%s\\n' '{\"type\":\"frame\",\"frame\":42,\"progress\":0.5,\"status\":\"tracking\",\"jpeg\":\"/9j/2Q==\"}'\n"
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
	args, err := os.ReadFile(argsFile)
	if err != nil || !strings.Contains(string(args), "-original=true\n") {
		t.Fatalf("original event video is not enabled by default: %s %v", args, err)
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

func TestTrackingObjectNamesPersistAndStayInsideEvent(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Join(s.trackingEventsRoot(), "event-one")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(`{}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "track_names.json"), []byte(`{"2":"Existing"}`), 0640); err != nil {
		t.Fatal(err)
	}
	cropDir := filepath.Join(dir, "track_crops", "object_0032")
	if err := os.MkdirAll(cropDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cropDir, "frame_000010_000001000ms.jpg"), []byte("image"), 0640); err != nil {
		t.Fatal(err)
	}
	cropRes := httptest.NewRecorder()
	s.Handler().ServeHTTP(cropRes, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/objects/32/crops", nil))
	if cropRes.Code != 200 || !strings.Contains(cropRes.Body.String(), "frame_000010_000001000ms.jpg") {
		t.Fatalf("crop list: %d %s", cropRes.Code, cropRes.Body.String())
	}
	put := httptest.NewRecorder()
	s.Handler().ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/v1/tracking/events/event-one/names/32", strings.NewReader(`{"name":"Helicopter"}`)))
	if put.Code != 200 {
		t.Fatal(put.Code, put.Body.String())
	}
	get := httptest.NewRecorder()
	s.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/event-one/names", nil))
	var names map[string]string
	if err := json.Unmarshal(get.Body.Bytes(), &names); err != nil || names["32"] != "Helicopter" || names["2"] != "Existing" {
		t.Fatalf("names: %s (%v)", get.Body.String(), err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "track_names.json"))
	if err != nil || !strings.Contains(string(data), "Helicopter") {
		t.Fatalf("names not saved: %s %v", data, err)
	}
	invalid := httptest.NewRecorder()
	s.Handler().ServeHTTP(invalid, httptest.NewRequest(http.MethodPut, "/api/v1/tracking/events/event-one/names/0", strings.NewReader(`{"name":"bad"}`)))
	if invalid.Code != 400 {
		t.Fatalf("invalid id: %d", invalid.Code)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"secret":"safe"}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "track_names.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "track_names.json")); err != nil {
		t.Fatal(err)
	}
	blocked := httptest.NewRecorder()
	s.Handler().ServeHTTP(blocked, httptest.NewRequest(http.MethodPut, "/api/v1/tracking/events/event-one/names/1", strings.NewReader(`{"name":"bad"}`)))
	if blocked.Code == 200 {
		t.Fatal("followed names symlink outside event")
	}
	data, _ = os.ReadFile(outside)
	if string(data) != `{"secret":"safe"}` {
		t.Fatalf("outside file modified: %s", data)
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

func TestTrackingFullVideoIntegration(t *testing.T) {
	worker := os.Getenv("TRACKFLIGHTS_INTEGRATION_WORKER")
	if worker == "" {
		t.Skip("set TRACKFLIGHTS_INTEGRATION_WORKER to run the OpenCV integration test")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is required")
	}
	s := newTestServer(t)
	t.Setenv("TRACKFLIGHTS_TRACKING_WORKER", worker)
	if err := os.MkdirAll(s.trackingRoot(), 0750); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(s.trackingRoot(), "input.mp4")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x120:rate=10", "-frames:v", "30", "-c:v", "mpeg4", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create video: %v: %s", err, out)
	}
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/tracking/start", strings.NewReader(`{"mode":"file","input":"input.mp4","profile":"general","fullVideo":true}`)))
	if res.Code != 202 {
		t.Fatal(res.Code, res.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state := s.jobs.Get(job.ID)
		if state.State == "succeeded" {
			break
		}
		if state.State == "failed" || state.State == "cancelled" {
			t.Fatalf("tracking: %+v", state)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state := s.jobs.Get(job.ID); state.State != "succeeded" {
		s.jobs.Cancel(job.ID)
		t.Fatalf("tracking did not finish: %+v", state)
	}
	res = httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events", nil))
	var listing struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, item := range listing.Items {
		if strings.HasPrefix(item.ID, "session-") {
			id = item.ID
		}
	}
	if id == "" {
		t.Fatalf("missing session: %s", res.Body.String())
	}
	output := filepath.Join(s.trackingEventsRoot(), id, "tracked.mp4")
	metadata, err := os.ReadFile(filepath.Join(s.trackingEventsRoot(), id, "tracking.json"))
	if err != nil {
		t.Fatalf("full session tracking metadata: %v", err)
	}
	var tracking struct {
		Frames []json.RawMessage `json:"frames"`
	}
	if err := json.Unmarshal(metadata, &tracking); err != nil || len(tracking.Frames) != 30 {
		t.Fatalf("full session tracking frames: %d, %v", len(tracking.Frames), err)
	}
	probe := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-count_frames", "-show_entries", "stream=codec_name,nb_read_frames", "-of", "csv=p=0", output)
	if out, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "h264,30" {
		t.Fatalf("unexpected video: %v: %s", err, out)
	}
	res = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tracking/events/"+id+"/files/tracked.mp4", nil)
	request.Header.Set("Range", "bytes=0-31")
	s.Handler().ServeHTTP(res, request)
	if res.Code != http.StatusPartialContent || res.Body.Len() != 32 {
		t.Fatalf("video range request: %d %d", res.Code, res.Body.Len())
	}
}
