package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type photoTransport func(*http.Request) (*http.Response, error)

func (f photoTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAircraftPhotoUsesOriginalContactAndFallsBackToRegistration(t *testing.T) {
	var paths []string
	client := &http.Client{Transport: photoTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("User-Agent") != aircraftPhotoUserAgent {
			t.Errorf("missing contact-bearing User-Agent: %q", r.Header.Get("User-Agent"))
		}
		paths = append(paths, r.URL.String())
		body, contentType := `{"photos":[]}`, "application/json"
		switch r.URL.String() {
		case "https://api.planespotters.net/pub/photos/reg/EI-EMA":
			body = `{"photos":[{"thumbnail_large":{"src":"https://t.plnspttrs.net/aircraft.jpg"}}]}`
		case "https://t.plnspttrs.net/aircraft.jpg":
			body, contentType = "image bytes", "image/jpeg"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	response := httptest.NewRecorder()
	flightPhotoWithClient(response, httptest.NewRequest(http.MethodGet, "/api/v1/flights/photo?icao=4ca849&registration=EI-EMA", nil), client)
	if response.Code != http.StatusOK || response.Body.String() != "image bytes" || len(paths) != 3 {
		t.Fatalf("unexpected photo response: status=%d body=%q paths=%v", response.Code, response.Body.String(), paths)
	}
}
