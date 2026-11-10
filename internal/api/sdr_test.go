package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSDRSignalStatusInLiveAPI(t *testing.T) {
	s := newTestServer(t)
	check := func(want string) {
		t.Helper()
		r := httptest.NewRecorder()
		s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/flights/live", nil))
		if !strings.Contains(r.Body.String(), `"sdrSignal":"`+want+`"`) {
			t.Fatalf("expected %s: %s", want, r.Body.String())
		}
	}
	check("disconnected")
	for _, status := range []string{"receiving", "no_signal", "receiver_unavailable", "unexpected"} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/sdr/frames", strings.NewReader("[]"))
		request.Header.Set("X-SDR-Signal", status)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatal(response.Code)
		}
		want := status
		if status == "unexpected" {
			want = "unknown"
		}
		check(want)
	}
	s.sdrMu.Lock()
	s.sdrLastContact = time.Now().Add(-16 * time.Second)
	s.sdrMu.Unlock()
	check("disconnected")
}
