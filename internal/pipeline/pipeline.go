package pipeline

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phonevoxel/internal/config"
	"github.com/phonevoxel/internal/geo"
	"github.com/phonevoxel/internal/types"
	"github.com/phonevoxel/internal/voxel"
)

// Client is a registered phone client with its own processing goroutine.
type Client struct {
	ID    string
	Inbox chan *types.ClientFrame

	mu       sync.RWMutex
	status   types.ClientStatus
	lastFPS  [16]float64
	fpsIdx   int
	lastPose geo.CameraPose
}

// Pipeline manages all connected clients and the shared voxel grid.
type Pipeline struct {
	mu      sync.RWMutex
	clients map[string]*Client

	grid *voxel.Grid
	geo  *geo.Converter

	motionThreshold    atomic.Int64  // stored as int (0-255)
	detectionThreshold atomic.Value  // float64

	cfg config.PipelineConfig
}

// New creates a Pipeline with the given grid and config.
func New(g *voxel.Grid, conv *geo.Converter, cfg config.PipelineConfig) *Pipeline {
	p := &Pipeline{
		clients: make(map[string]*Client),
		grid:    g,
		geo:     conv,
		cfg:     cfg,
	}
	p.motionThreshold.Store(int64(cfg.MotionThreshold))
	p.detectionThreshold.Store(cfg.DetectionThreshold)
	return p
}

// RegisterClient creates and starts a per-client processing goroutine.
// clientType should be "phone" or "webcam".
func (p *Pipeline) RegisterClient(ctx context.Context, id, clientType string) *Client {
	c := &Client{
		ID:    id,
		Inbox: make(chan *types.ClientFrame, 16),
		status: types.ClientStatus{
			ID:   id,
			Type: clientType,
		},
	}

	p.mu.Lock()
	p.clients[id] = c
	p.mu.Unlock()

	go p.runClient(ctx, c)
	return c
}

// UnregisterClient removes a client from the pipeline.
func (p *Pipeline) UnregisterClient(id string) {
	p.mu.Lock()
	delete(p.clients, id)
	p.mu.Unlock()
}

// ClientStatuses returns a snapshot of all connected client statuses.
func (p *Pipeline) ClientStatuses() []types.ClientStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]types.ClientStatus, 0, len(p.clients))
	for _, c := range p.clients {
		c.mu.RLock()
		out = append(out, c.status)
		c.mu.RUnlock()
	}
	return out
}

// GetClient returns the Client struct for the given id, or nil.
// The caller may send frames to Client.Inbox directly.
func (p *Pipeline) GetClient(id string) *Client {
	p.mu.RLock()
	c := p.clients[id]
	p.mu.RUnlock()
	return c
}

// ClientPose returns the most recent camera pose for the named client.
// ok is false if the client does not exist.
func (p *Pipeline) ClientPose(id string) (geo.CameraPose, bool) {
	p.mu.RLock()
	c, ok := p.clients[id]
	p.mu.RUnlock()
	if !ok {
		return geo.CameraPose{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastPose, true
}

// SetMotionThreshold updates the motion threshold live.
func (p *Pipeline) SetMotionThreshold(t int) {
	p.motionThreshold.Store(int64(t))
}

// SetDetectionThreshold updates the detection threshold live.
func (p *Pipeline) SetDetectionThreshold(t float64) {
	p.detectionThreshold.Store(t)
}

// DetectionThreshold returns the current detection threshold.
func (p *Pipeline) DetectionThreshold() float64 {
	return p.detectionThreshold.Load().(float64)
}

// runClient is the per-client processing goroutine.
func (p *Pipeline) runClient(ctx context.Context, c *Client) {
	var prevGray []uint8
	var frameCount int
	var lastFPSTime = time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-c.Inbox:
			if !ok {
				return
			}
			p.processFrame(c, frame, &prevGray, &frameCount, &lastFPSTime)
		}
	}
}

func (p *Pipeline) processFrame(
	c *Client,
	frame *types.ClientFrame,
	prevGray *[]uint8,
	frameCount *int,
	lastFPSTime *time.Time,
) {
	// Decode JPEG
	img, err := jpeg.Decode(bytes.NewReader(frame.JPEG))
	if err != nil {
		log.Printf("client %s: JPEG decode error: %v", c.ID, err)
		return
	}

	bounds := img.Bounds()
	w := bounds.Max.X - bounds.Min.X
	h := bounds.Max.Y - bounds.Min.Y

	// Ensure origin is set
	if !p.geo.IsSet() {
		p.geo.SetOrigin(frame.Lat, frame.Lon, frame.Alt)
		log.Printf("ENU origin set to %.6f, %.6f, %.2f from client %s",
			frame.Lat, frame.Lon, frame.Alt, c.ID)
	}

	// Compute camera intrinsics from client-reported FOV
	hfov := frame.HFOV
	if hfov <= 0 {
		hfov = 70.0
	}
	intr := geo.IntrinsicsFromHFOV(w, h, hfov)

	// Compute camera pose — webcam uses azimuth/elevation, phone uses W3C angles.
	var pose geo.CameraPose
	if frame.UseAzimuthElevation {
		pose = geo.ComputePoseFromAzimuthElevation(p.geo,
			frame.Lat, frame.Lon, frame.Alt,
			frame.Alpha, frame.Elevation, frame.Gamma)
	} else {
		pose = geo.ComputePose(p.geo, frame.Lat, frame.Lon, frame.Alt,
			frame.Alpha, frame.Beta, frame.Gamma)
	}

	// Convert to grayscale
	gray := toGrayscale(img, w, h)

	// Frame differencing
	motionThreshold := uint8(p.motionThreshold.Load())
	if *prevGray != nil && len(*prevGray) == len(gray) {
		diff := absDiff(gray, *prevGray)
		motionPixels := thresholdPixels(diff, w, h, motionThreshold)

		// Project motion rays into voxel grid
		for _, px := range motionPixels {
			dir := geo.PixelToRayENU(float64(px.X), float64(px.Y), intr, pose)
			ray := voxel.NewRay(pose.East, pose.North, pose.Up, dir[0], dir[1], dir[2])
			magnitude := float64(px.Value) / 255.0
			p.grid.AccumulateRay(ray, magnitude)
		}
	}

	*prevGray = gray

	// Update client status
	*frameCount++
	now := time.Now()
	elapsed := now.Sub(*lastFPSTime).Seconds()
	if elapsed >= 1.0 {
		fps := float64(*frameCount) / elapsed
		*frameCount = 0
		*lastFPSTime = now

		c.mu.Lock()
		c.status.FPS = fps
		c.status.Lat = frame.Lat
		c.status.Lon = frame.Lon
		c.status.Heading = frame.Alpha
		c.status.East = pose.East
		c.status.North = pose.North
		c.status.Up = pose.Up
		c.status.Elevation = frame.Elevation
		c.status.LastSeen = float64(now.UnixMilli()) / 1000.0
		c.status.GPSAccuracy = frame.GPSAccuracy
		c.lastPose = pose
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		c.status.Lat = frame.Lat
		c.status.Lon = frame.Lon
		c.status.Heading = frame.Alpha
		c.status.East = pose.East
		c.status.North = pose.North
		c.status.Up = pose.Up
		c.status.Elevation = frame.Elevation
		c.status.LastSeen = float64(now.UnixMilli()) / 1000.0
		c.status.GPSAccuracy = frame.GPSAccuracy
		c.lastPose = pose
		c.mu.Unlock()
	}
}

// toGrayscale converts an image to a flat uint8 grayscale array.
func toGrayscale(img image.Image, w, h int) []uint8 {
	gray := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// Standard luminance formula, values are 0-65535 from RGBA()
			lum := (299*r + 587*g + 114*b) / 1000
			gray[y*w+x] = uint8(lum >> 8)
		}
	}
	return gray
}

// absDiff computes the per-pixel absolute difference between two grayscale frames.
func absDiff(a, b []uint8) []uint8 {
	out := make([]uint8, len(a))
	for i := range a {
		diff := int(a[i]) - int(b[i])
		if diff < 0 {
			diff = -diff
		}
		out[i] = uint8(diff)
	}
	return out
}

// motionPixel is a pixel whose diff value exceeded the threshold.
type motionPixel struct {
	X, Y  int
	Value uint8
}

// thresholdPixels returns pixels in a diff image above the motion threshold.
func thresholdPixels(diff []uint8, w, h int, threshold uint8) []motionPixel {
	var out []motionPixel
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := diff[y*w+x]
			if v > threshold {
				out = append(out, motionPixel{X: x, Y: y, Value: v})
			}
		}
	}
	return out
}
