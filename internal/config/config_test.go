package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyHomeMigrationPreservesConfiguredCoordinates(t *testing.T) {
	for _, test := range []struct {
		name                        string
		latitude, longitude         float64
		home                        string
		wantLatitude, wantLongitude float64
	}{
		{"unconfigured legacy home", 0, 0, "", 59.323316, 18.480349},
		{"configured legacy home", 51.5, -0.1, "", 51.5, -0.1},
		{"explicit zero coordinates", 0, 0, "My receiver", 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			cfg.Flight.Latitude, cfg.Flight.Longitude, cfg.Flight.HomeName = test.latitude, test.longitude, test.home
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := NewStore(path).Load()
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Flight.Latitude != test.wantLatitude || loaded.Flight.Longitude != test.wantLongitude {
				t.Fatalf("unexpected location: %+v", loaded.Flight)
			}
		})
	}
}

func TestAircraftExpiryDefaultsValidatesAndPersists(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	cfg := Default()
	cfg.Flight.AircraftExpirySeconds = nil // Existing configurations omit the field.
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Flight.AircraftExpiry() != 5*time.Minute {
		t.Fatalf("legacy default: %v", loaded.Flight.AircraftExpiry())
	}
	seconds := 720
	loaded.Flight.AircraftExpirySeconds = &seconds
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Flight.AircraftExpiry() != 12*time.Minute {
		t.Fatalf("saved expiry: %v", loaded.Flight.AircraftExpiry())
	}
	for _, seconds := range []int{0, -1, 86401} {
		loaded.Flight.AircraftExpirySeconds = &seconds
		if err := store.Save(loaded); err == nil {
			t.Fatalf("accepted invalid expiry %d", seconds)
		}
	}
}

func TestTerrainHeightMultiplierDefaultsAndValidation(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	cfg := Default()
	cfg.Flight.TerrainHeightMultiplier = nil
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Flight.TerrainHeightMultiplier == nil || *loaded.Flight.TerrainHeightMultiplier != 1 {
		t.Fatalf("legacy default: %v", loaded.Flight.TerrainHeightMultiplier)
	}
	multiplier := 3.5
	loaded.Flight.TerrainHeightMultiplier = &multiplier
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if *loaded.Flight.TerrainHeightMultiplier != 3.5 {
		t.Fatalf("saved multiplier: %v", loaded.Flight.TerrainHeightMultiplier)
	}
	for _, multiplier := range []float64{-1, 0, 0.09, 20.1} {
		loaded.Flight.TerrainHeightMultiplier = &multiplier
		if err := store.Save(loaded); err == nil {
			t.Fatalf("accepted multiplier %v", multiplier)
		}
	}
}

func TestNostrRelayConfiguration(t *testing.T) {
	cfg := Default()
	if cfg.Flight.NostrEnabled == nil || !*cfg.Flight.NostrEnabled || cfg.Flight.NostrRelayURL != "ws://127.0.0.1:7447" {
		t.Fatal("incorrect default relay settings")
	}
	for _, url := range []string{"http://example.com", "ws://user:password@example.com", "ws://example.com/?token=secret", ""} {
		cfg.Flight.NostrRelayURL = url
		if cfg.Validate() == nil {
			t.Fatalf("accepted invalid relay URL %q", url)
		}
	}
	cfg.Flight.NostrRelayURL = "wss://relay.example.com"
	cfg.Flight.NostrPublisher = "invalid"
	if cfg.Validate() == nil {
		t.Fatal("accepted invalid public key")
	}
	cfg.Flight.NostrPublisher = ""
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	disabled := false
	cfg.Flight.NostrEnabled = &disabled
	cfg.Flight.NostrRelayURL = ""
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTrackingNostrDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	cfg.Tracking = TrackingConfig{OutputDir: "events"}
	applyDefaults(&cfg)
	if cfg.Tracking.NostrEnabled == nil || !*cfg.Tracking.NostrEnabled || cfg.Tracking.NostrAutoStart == nil || !*cfg.Tracking.NostrAutoStart || cfg.Tracking.NostrRelayURL != "ws://127.0.0.1:7448" || cfg.Tracking.NostrKeyFile != "tracking/nostr.key" {
		t.Fatalf("bad legacy defaults: %+v", cfg.Tracking)
	}
	for _, address := range []string{"", "http://localhost:7448", "ws://user:password@localhost:7448", "ws://localhost:7448/?key=secret"} {
		cfg.Tracking.NostrRelayURL = address
		if cfg.Validate() == nil {
			t.Fatalf("accepted invalid tracking relay %q", address)
		}
	}
	cfg.Tracking.NostrRelayURL = "ws://localhost:7448"
	cfg.Tracking.NostrKeyFile = ""
	if cfg.Validate() == nil {
		t.Fatal("accepted missing key path")
	}
	cfg.Tracking.NostrEnabled = boolPointer(false)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled publication should not need a key: %v", err)
	}
}

func TestBlossomURLDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	cfg.Tracking.BlossomURL = ""
	applyDefaults(&cfg)
	if cfg.Tracking.BlossomURL != "http://127.0.0.1:3001" {
		t.Fatal(cfg.Tracking.BlossomURL)
	}
	for _, value := range []string{"javascript:alert(1)", "http://user:pass@example.org", "http://example.org/?secret=x"} {
		cfg.Tracking.BlossomURL = value
		if cfg.Validate() == nil {
			t.Fatalf("accepted invalid Blossom URL %q", value)
		}
	}
	cfg.Tracking.BlossomURL = "https://blossom.example.org"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTrackingNostrTravelDistanceDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	cfg.Tracking.NostrMinTrackDistance = nil
	applyDefaults(&cfg)
	if cfg.Tracking.NostrMinTrackDistance == nil || *cfg.Tracking.NostrMinTrackDistance != 800 {
		t.Fatal("old tracker default was not applied")
	}
	zero := 0.0
	cfg.Tracking.NostrMinTrackDistance = &zero
	if err := cfg.Validate(); err != nil {
		t.Fatalf("zero must include all tracks: %v", err)
	}
	negative := -1.0
	cfg.Tracking.NostrMinTrackDistance = &negative
	if cfg.Validate() == nil {
		t.Fatal("accepted a negative distance")
	}
}

func TestOverlayAppearanceDefaultsAndPersistence(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	cfg := Default()
	cfg.Tracking.OverlayBoxScale, cfg.Tracking.OverlayBoxThickness, cfg.Tracking.OverlayPathThickness = nil, nil, nil
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if *loaded.Tracking.OverlayBoxScale != 4 || *loaded.Tracking.OverlayBoxThickness != 1.5 || *loaded.Tracking.OverlayPathThickness != 1 {
		t.Fatalf("wrong legacy defaults: %+v", loaded.Tracking)
	}
	loaded.Tracking.OverlayBoxScale = floatPointer(6)
	loaded.Tracking.OverlayBoxThickness = floatPointer(0.75)
	loaded.Tracking.OverlayPathThickness = floatPointer(0.5)
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if *loaded.Tracking.OverlayBoxScale != 6 || *loaded.Tracking.OverlayBoxThickness != 0.75 || *loaded.Tracking.OverlayPathThickness != 0.5 {
		t.Fatalf("settings did not persist: %+v", loaded.Tracking)
	}
	loaded.Tracking.OverlayBoxScale = floatPointer(0)
	if err := loaded.Validate(); err == nil {
		t.Fatal("accepted invalid box scale")
	}
	loaded.Tracking.OverlayBoxScale = floatPointer(4)
	loaded.Tracking.OverlayBoxThickness = floatPointer(0)
	if err := loaded.Validate(); err == nil {
		t.Fatal("accepted invalid box thickness")
	}
	loaded.Tracking.OverlayBoxThickness = floatPointer(1)
	loaded.Tracking.OverlayPathThickness = floatPointer(13)
	if err := loaded.Validate(); err == nil {
		t.Fatal("accepted invalid path thickness")
	}
}
