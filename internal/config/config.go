package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const CurrentVersion = 1

type Config struct {
	Version  int            `json:"version"`
	Server   ServerConfig   `json:"server"`
	Flight   FlightConfig   `json:"flight"`
	Dwarf    DwarfConfig    `json:"dwarf"`
	Tracking TrackingConfig `json:"tracking"`
}

type ServerConfig struct {
	ListenAddress string `json:"listenAddress"`
	AccessMode    string `json:"accessMode"`
}

type FlightConfig struct {
	Device                 string  `json:"device"`
	Gain                   string  `json:"gain"`
	PPM                    int     `json:"ppm"`
	Latitude               float64 `json:"latitude"`
	Longitude              float64 `json:"longitude"`
	TrackingDir            string  `json:"trackingDir"`
	HomeName               string  `json:"homeName"`
	TileURL                string  `json:"tileUrl"`
	TerrainURL             string  `json:"terrainUrl"`
	ShowAirports           bool    `json:"showAirports"`
	HostBellEnabled        bool    `json:"hostBellEnabled"`
	HistoryLookbackMinutes *int    `json:"historyLookbackMinutes"`
	HistoryBaseURL         string  `json:"historyBaseUrl"`
	AircraftExpirySeconds  *int    `json:"aircraftExpirySeconds"`
}

func (c FlightConfig) AircraftExpiry() time.Duration {
	if c.AircraftExpirySeconds == nil {
		return 5 * time.Minute
	}
	return time.Duration(*c.AircraftExpirySeconds) * time.Second
}

type DwarfConfig struct {
	Host   string `json:"host"`
	Camera string `json:"camera"`
}

type TrackingConfig struct {
	OutputDir string `json:"outputDir"`
}

func Default() Config {
	return Config{
		Version:  CurrentVersion,
		Server:   ServerConfig{ListenAddress: "127.0.0.1:8080", AccessMode: "loopback"},
		Flight:   FlightConfig{Device: "0", Gain: "auto", TrackingDir: "flights", HomeName: "Tottkällevägen 4A, Värmdö", Latitude: 59.323316, Longitude: 18.480349, TileURL: "https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}", TerrainURL: "https://elevation3d.arcgis.com/arcgis/rest/services/WorldElevation3D/Terrain3D/ImageServer", ShowAirports: true, HistoryLookbackMinutes: intPointer(5), HistoryBaseURL: "https://adsb.lol", AircraftExpirySeconds: intPointer(300)},
		Dwarf:    DwarfConfig{Host: "192.168.88.1", Camera: "tele"},
		Tracking: TrackingConfig{OutputDir: "events"},
	}
}

func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if strings.TrimSpace(c.Server.ListenAddress) == "" {
		return errors.New("server listen address is required")
	}
	if c.Server.AccessMode != "loopback" && c.Server.AccessMode != "lan" {
		return errors.New("server access mode must be loopback or lan")
	}
	if c.Dwarf.Camera != "tele" && c.Dwarf.Camera != "wide" {
		return errors.New("DWARF camera must be tele or wide")
	}
	if c.Flight.Latitude < -90 || c.Flight.Latitude > 90 || c.Flight.Longitude < -180 || c.Flight.Longitude > 180 {
		return errors.New("flight receiver coordinates are invalid")
	}
	if c.Flight.HistoryLookbackMinutes != nil && (*c.Flight.HistoryLookbackMinutes < 0 || *c.Flight.HistoryLookbackMinutes > 1440) {
		return errors.New("flight history lookback must be between 0 and 1440 minutes")
	}
	if c.Flight.AircraftExpirySeconds != nil && (*c.Flight.AircraftExpirySeconds < 1 || *c.Flight.AircraftExpirySeconds > 86400) {
		return errors.New("aircraft visibility after the last signal must be between 1 and 86400 seconds")
	}
	if c.Flight.HistoryBaseURL != "" {
		parsed, err := url.Parse(c.Flight.HistoryBaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("invalid flight history service URL")
		}
	}
	return nil
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		return cfg, s.writeLocked(cfg)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	defaults := Default().Flight
	legacyMapConfig := cfg.Flight.HomeName == ""
	if legacyMapConfig {
		cfg.Flight.HomeName = defaults.HomeName
		// The pre-map migration stored 0,0 to mean "not configured". Restore
		// the legacy tracker home without replacing a configured location.
		if cfg.Flight.Latitude == 0 && cfg.Flight.Longitude == 0 {
			cfg.Flight.Latitude, cfg.Flight.Longitude = defaults.Latitude, defaults.Longitude
		}
	}
	if cfg.Flight.TileURL == "" {
		cfg.Flight.TileURL = defaults.TileURL
	}
	if cfg.Flight.TerrainURL == "" {
		cfg.Flight.TerrainURL = defaults.TerrainURL
	}
	if cfg.Flight.HistoryLookbackMinutes == nil {
		cfg.Flight.HistoryLookbackMinutes = intPointer(5)
	}
	if cfg.Flight.AircraftExpirySeconds == nil {
		cfg.Flight.AircraftExpirySeconds = intPointer(300)
	}
	if cfg.Flight.HistoryBaseURL == "" {
		cfg.Flight.HistoryBaseURL = defaults.HistoryBaseURL
	}
	if legacyMapConfig {
		cfg.Flight.ShowAirports = defaults.ShowAirports
	}
}

func intPointer(value int) *int { return &value }

func (s *Store) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := cfg.Validate(); err != nil {
		return err
	}
	return s.writeLocked(cfg)
}

func (s *Store) writeLocked(cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
