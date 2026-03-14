package types

import "time"

// ClientFrame is a decoded message from a phone client.
// The binary wire format is defined in the spec; this is the parsed Go struct.
type ClientFrame struct {
	// Identity
	ClientID string

	// Timing
	ClientTimestamp float64   // Unix seconds from client
	ServerTimestamp time.Time // time the server received this frame

	// GPS
	Lat      float64
	Lon      float64
	Alt      float64
	GPSAccuracy float64

	// Device orientation (W3C DeviceOrientationEvent convention)
	Alpha float64 // compass heading, degrees (0-360)
	Beta  float64 // pitch, degrees (-180 to 180)
	Gamma float64 // roll, degrees (-90 to 90)

	// Camera
	HFOV        float64 // horizontal field of view, degrees
	FrameWidth  uint32
	FrameHeight uint32

	// Raw JPEG bytes
	JPEG []byte

	// UseAzimuthElevation, when true, instructs the pipeline to build the camera
	// pose from Azimuth/Elevation/Roll instead of the W3C Alpha/Beta/Gamma angles.
	// Set by the webcam path; phone clients leave this false.
	UseAzimuthElevation bool
	Elevation           float64 // degrees above horizontal (webcam only)
}

// ClientStatus is the per-client state sent to dashboard viewers.
type ClientStatus struct {
	ID          string  `json:"id"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Heading     float64 `json:"heading"` // alpha
	LastSeen    float64 `json:"last_seen"`
	FPS         float64 `json:"fps"`
	GPSAccuracy float64 `json:"gps_accuracy"`
}

// Target represents a detected motion cluster in the voxel grid.
type Target struct {
	Position   [3]float64 `json:"position"`   // ENU meters
	Confidence float64    `json:"confidence"`
	Velocity   [3]float64 `json:"velocity"`   // estimated m/s
	LatLon     [2]float64 `json:"latlon"`     // GPS lat, lon
}

// DashboardUpdate is the periodic snapshot pushed to dashboard WebSocket clients.
type DashboardUpdate struct {
	Timestamp   float64        `json:"timestamp"`
	Clients     []ClientStatus `json:"clients"`
	Targets     []Target       `json:"targets"`
	SparseVoxels []SparseVoxel `json:"sparse_voxels,omitempty"`
	GridMeta    GridMeta       `json:"grid_meta"`
}

// SparseVoxel is a single above-threshold voxel in the grid, in grid-index space.
type SparseVoxel struct {
	IX    int     `json:"ix"`
	IY    int     `json:"iy"`
	IZ    int     `json:"iz"`
	Value float64 `json:"v"`
}

// GridMeta describes grid dimensions so the dashboard can reconstruct positions.
type GridMeta struct {
	MinE       float64 `json:"min_east"`
	MaxE       float64 `json:"max_east"`
	MinN       float64 `json:"min_north"`
	MaxN       float64 `json:"max_north"`
	MinU       float64 `json:"min_up"`
	MaxU       float64 `json:"max_up"`
	Resolution float64 `json:"resolution"`
	NX         int     `json:"nx"`
	NY         int     `json:"ny"`
	NZ         int     `json:"nz"`
}

// RayQueryRequest is sent from dashboard to server to locate a target via a pixel click.
type RayQueryRequest struct {
	ClientID string  `json:"client_id"`
	PixelX   float64 `json:"pixel_x"`
	PixelY   float64 `json:"pixel_y"`
}

// RayQueryResponse is the server's response to a manual ray query.
type RayQueryResponse struct {
	Position [3]float64 `json:"position"` // ENU meters
	LatLon   [2]float64 `json:"latlon"`
	Value    float64    `json:"value"`
	Found    bool       `json:"found"`
}

// ConfigUpdateRequest allows the dashboard to hot-reload grid/pipeline parameters.
type ConfigUpdateRequest struct {
	DecayFactor        *float64 `json:"decay_factor,omitempty"`
	DecayInterval      *int     `json:"decay_interval_ms,omitempty"`
	MotionThreshold    *int     `json:"motion_threshold,omitempty"`
	DetectionThreshold *float64 `json:"detection_threshold,omitempty"`
}

// HeaderSize is the fixed binary header size in bytes for client→server messages.
const HeaderSize = 84

// WebcamStateFile is the persisted list of webcam camera configurations.
type WebcamStateFile struct {
	Cameras []WebcamCameraEntry `json:"cameras"`
}

// WebcamCameraEntry mirrors config.WebcamCamera but is self-contained so the
// types package doesn't import config (avoiding import cycles).
type WebcamCameraEntry struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Device    string  `json:"device"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FPS       int     `json:"fps"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	AltMeters float64 `json:"alt_meters"`
	Azimuth   float64 `json:"azimuth"`
	Elevation float64 `json:"elevation"`
	Roll      float64 `json:"roll"`
	HFOV      float64 `json:"hfov"`
}
