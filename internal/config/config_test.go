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
