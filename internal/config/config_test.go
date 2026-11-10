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
