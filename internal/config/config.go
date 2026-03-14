package config

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"time"
)

// Config is the top-level server configuration.
type Config struct {
	Server    ServerConfig    `json:"server"`
	Grid      GridConfig      `json:"grid"`
	Pipeline  PipelineConfig  `json:"pipeline"`
	Camera    CameraConfig    `json:"camera"`
	Dashboard DashboardConfig `json:"dashboard"`
	Webcam    WebcamConfig    `json:"webcam"`
}

// ServerConfig holds HTTP/TLS settings.
type ServerConfig struct {
	Addr     string `json:"addr"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// GridConfig defines the voxel grid dimensions and decay.
type GridConfig struct {
	MinE float64 `json:"min_east"`
	MaxE float64 `json:"max_east"`
	MinN float64 `json:"min_north"`
	MaxN float64 `json:"max_north"`
	MinU float64 `json:"min_up"`
	MaxU float64 `json:"max_up"`

	Resolution float64 `json:"resolution"`

	OriginMode string  `json:"origin_mode"` // "auto" | "manual"
	OriginLat  float64 `json:"origin_lat"`
	OriginLon  float64 `json:"origin_lon"`
	OriginAlt  float64 `json:"origin_alt"`

	DecayFactor   float64 `json:"decay_factor"`
	DecayInterval int     `json:"decay_interval_ms"`
}

// PipelineConfig holds per-frame processing settings.
type PipelineConfig struct {
	MotionThreshold    int     `json:"motion_threshold"`
	DetectionThreshold float64 `json:"detection_threshold"`
	MaxClients         int     `json:"max_clients"`
}

// CameraConfig holds camera defaults.
type CameraConfig struct {
	DefaultHFOVDegrees float64 `json:"default_hfov_degrees"`
}

// WebcamConfig holds settings for the server-side webcam capture mode.
type WebcamConfig struct {
	// StatePath is the JSON file where camera configurations are persisted.
	StatePath string `json:"state_path"`
	// SyncHz is the rate at which all cameras are sampled simultaneously.
	SyncHz float64 `json:"sync_hz"`
	// FeedJPEGQuality is the MJPEG quality (1–95) used for browser feed streams.
	FeedJPEGQuality int `json:"feed_jpeg_quality"`
	// MotionAmplify multiplies diff pixel values before encoding the motion feed.
	MotionAmplify int `json:"motion_amplify"`
}

// WebcamCamera is a single configured webcam with its physical pose on the map.
type WebcamCamera struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Device    string  `json:"device"` // e.g. /dev/video0 (Linux), 0 (macOS), "Camera Name" (Windows)
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FPS       int     `json:"fps"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	AltMeters float64 `json:"alt_meters"`
	Azimuth   float64 `json:"azimuth"`   // degrees clockwise from north
	Elevation float64 `json:"elevation"` // degrees above horizontal
	Roll      float64 `json:"roll"`      // degrees, default 0
	HFOV      float64 `json:"hfov"`      // horizontal field of view, degrees
}

// DashboardConfig holds dashboard broadcast settings.
type DashboardConfig struct {
	UpdateRateHz    float64 `json:"update_rate_hz"`
	SparseThreshold float64 `json:"sparse_threshold"`
}

// Defaults returns a Config populated with safe default values.
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Addr:     ":8443",
			CertFile: "cert.pem",
			KeyFile:  "key.pem",
		},
		Grid: GridConfig{
			MinE: -100, MaxE: 100,
			MinN: -100, MaxN: 100,
			MinU: -5, MaxU: 50,
			Resolution:    1.0,
			OriginMode:    "auto",
			DecayFactor:   0.95,
			DecayInterval: 250,
		},
		Pipeline: PipelineConfig{
			MotionThreshold:    15,
			DetectionThreshold: 0.5,
			MaxClients:         20,
		},
		Camera: CameraConfig{
			DefaultHFOVDegrees: 70.0,
		},
		Webcam: WebcamConfig{
			StatePath:       "webcam_cameras.json",
			SyncHz:          30.0,
			FeedJPEGQuality: 70,
			MotionAmplify:   6,
		},
		Dashboard: DashboardConfig{
			UpdateRateHz:    4.0,
			SparseThreshold: 0.05,
		},
	}
}

// Load parses command-line flags and optional JSON config file.
func Load() Config {
	cfgPath := flag.String("config", "", "Path to JSON config file (optional)")
	addr := flag.String("addr", "", "Server listen address (overrides config)")
	cert := flag.String("cert", "", "TLS cert file (overrides config)")
	key := flag.String("key", "", "TLS key file (overrides config)")
	flag.Parse()

	cfg := Defaults()

	if *cfgPath != "" {
		data, err := os.ReadFile(*cfgPath)
		if err != nil {
			log.Fatalf("Failed to read config file %s: %v", *cfgPath, err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Fatalf("Failed to parse config file: %v", err)
		}
	}

	if *addr != "" {
		cfg.Server.Addr = *addr
	}
	if *cert != "" {
		cfg.Server.CertFile = *cert
	}
	if *key != "" {
		cfg.Server.KeyFile = *key
	}

	return cfg
}

// DecayDuration converts DecayInterval to a time.Duration.
func (g GridConfig) DecayDuration() time.Duration {
	return time.Duration(g.DecayInterval) * time.Millisecond
}
