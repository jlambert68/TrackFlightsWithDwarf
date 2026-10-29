package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jlambert/trackflights/internal/flight/flighthistory"
)

func (s *Server) historyClient(base string) *flighthistory.Client {
	if base == "" {
		base = "https://adsb.lol"
	}
	base = strings.TrimRight(base, "/")
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	if s.history == nil || s.history.BaseURL != base {
		s.history = flighthistory.NewClient(base)
	}
	return s.history
}

func (s *Server) flightTrace(w http.ResponseWriter, r *http.Request) {
	icao := strings.TrimSpace(r.URL.Query().Get("icao"))
	before, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("before"))
	if len(icao) != 6 || err != nil || before.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid ICAO and before time are required"})
		return
	}
	if _, err := hex.DecodeString(icao); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ICAO"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	settings := s.currentConfig().Flight
	minutes := settings.HistoryLookbackMinutes
	lookback := 5 * time.Minute
	if minutes != nil {
		lookback = time.Duration(*minutes) * time.Minute
	}
	points, err := s.historyClient(settings.HistoryBaseURL).Lookup(ctx, icao, before, lookback)
	if err != nil {
		message := "External flight history unavailable"
		if strings.Contains(err.Error(), "403 Forbidden") {
			message = "History service rejected the request (HTTP 403)"
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": message})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lookbackSeconds": int64(lookback / time.Second), "points": points})
}

func (s *Server) flightMetadata(w http.ResponseWriter, r *http.Request) {
	icao := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("icao")))
	callsign := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("callsign")))
	if len(icao) != 6 || len(callsign) > 12 || strings.Trim(callsign, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 ") != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid aircraft identifier"})
		return
	}
	if _, err := hex.DecodeString(icao); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ICAO"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	info, infoErr := s.metadata.Lookup(ctx, icao)
	values := map[string]string{}
	keys := []string{"registration", "manufacturer", "model", "typecode", "operator", "country"}
	for i, key := range keys {
		start := strings.Index(info, key+"=")
		if start < 0 {
			continue
		}
		value := info[start+len(key)+1:]
		for _, next := range keys[i+1:] {
			if end := strings.Index(value, " "+next+"="); end >= 0 {
				value = value[:end]
				break
			}
		}
		values[key] = strings.TrimSpace(value)
	}
	if infoErr != nil {
		values["metadataStatus"] = "Aircraft metadata unavailable"
	}
	if callsign != "" {
		route, err := s.routes.Lookup(ctx, callsign)
		if err == nil {
			values["departureAirport"], values["destinationAirport"] = route.Origin.String(), route.Destination.String()
		} else {
			values["routeStatus"] = "Route unavailable"
		}
	}
	writeJSON(w, http.StatusOK, values)
}

func (s *Server) flightPhoto(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" || !photoHost(req.URL.Hostname()) {
			return fmt.Errorf("unsupported redirect")
		}
		return nil
	}}
	flightPhotoWithClient(w, r, client)
}

const aircraftPhotoUserAgent = "SDR-FlightTracker/1.0 (+https://github.com/jlambert68/SDR-FlightTracker)"

func flightPhotoWithClient(w http.ResponseWriter, r *http.Request, client *http.Client) {
	icao := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("icao")))
	if len(icao) != 6 {
		http.Error(w, "invalid ICAO", http.StatusBadRequest)
		return
	}
	if _, err := hex.DecodeString(icao); err != nil {
		http.Error(w, "invalid ICAO", http.StatusBadRequest)
		return
	}
	registration := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("registration")))
	if len(registration) > 12 || strings.Trim(registration, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
		http.Error(w, "invalid registration", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	endpoints := []string{"https://api.planespotters.net/pub/photos/hex/" + icao}
	if registration != "" {
		endpoints = append(endpoints, "https://api.planespotters.net/pub/photos/reg/"+url.PathEscape(registration))
	}
	for _, endpoint := range endpoints {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		req.Header.Set("User-Agent", aircraftPhotoUserAgent)
		response, err := client.Do(req)
		if err != nil {
			continue
		}
		var result struct {
			Photos []struct {
				Thumbnail struct {
					Source string `json:"src"`
				} `json:"thumbnail"`
				Large struct {
					Source string `json:"src"`
				} `json:"thumbnail_large"`
			} `json:"photos"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || len(result.Photos) == 0 {
			continue
		}
		src := result.Photos[0].Large.Source
		if src == "" {
			src = result.Photos[0].Thumbnail.Source
		}
		parsed, err := url.Parse(src)
		if err != nil || parsed.Scheme != "https" || !photoHost(parsed.Hostname()) {
			continue
		}
		imageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			continue
		}
		imageReq.Header.Set("User-Agent", aircraftPhotoUserAgent)
		imageReq.Header.Set("Referer", "https://www.planespotters.net/")
		image, err := client.Do(imageReq)
		if err != nil {
			continue
		}
		if image.StatusCode != http.StatusOK || !strings.HasPrefix(image.Header.Get("Content-Type"), "image/") {
			image.Body.Close()
			continue
		}
		w.Header().Set("Content-Type", image.Header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = io.Copy(w, io.LimitReader(image.Body, 8<<20))
		image.Body.Close()
		return
	}
	http.Error(w, "aircraft photo unavailable", http.StatusNotFound)
}

func photoHost(host string) bool {
	host = strings.ToLower(host)
	return host == "planespotters.net" || strings.HasSuffix(host, ".planespotters.net") || host == "plnspttrs.net" || strings.HasSuffix(host, ".plnspttrs.net")
}
