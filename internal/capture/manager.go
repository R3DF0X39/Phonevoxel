// Package capture manages ffmpeg-based multi-camera acquisition with
// synchronized frame sampling across all cameras.
package capture

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// Frame is a captured frame with raw JPEG, motion JPEG, and timing metadata.
type Frame struct {
	JPEG       []byte    // raw camera frame
	MotionJPEG []byte    // amplified grayscale diff (nil until second frame)
	CapturedAt time.Time
	Width      int
	Height     int
}

// SyncFrame is delivered to the sync callback once per tick, per active camera.
type SyncFrame struct {
	CameraID string
	Frame    *Frame
}

// camera holds per-camera state.
type camera struct {
	id     string
	device string
	width  int
	height int
	fps    int

	mu       sync.RWMutex
	latest   *Frame
	prevGray []uint8 // previous grayscale pixels for motion diff

	cancel context.CancelFunc
	cmd    *exec.Cmd
}

// Manager orchestrates N cameras captured via ffmpeg with a synchronized tick.
type Manager struct {
	mu      sync.RWMutex
	cameras map[string]*camera

	syncInterval    time.Duration
	motionAmplify   int
	feedJPEGQuality int

	onSync func([]SyncFrame) // called every sync tick with all ready frames
}

// New creates a Manager.
//
//	syncHz          – synchronized sample rate (e.g. 30)
//	motionAmplify   – pixel multiplier for the motion-diff visualization feed
//	feedJPEGQuality – JPEG quality (1–95) for both raw and motion MJPEG streams
func New(syncHz float64, motionAmplify, feedJPEGQuality int) *Manager {
	interval := time.Duration(float64(time.Second) / syncHz)
	return &Manager{
		cameras:         make(map[string]*camera),
		syncInterval:    interval,
		motionAmplify:   motionAmplify,
		feedJPEGQuality: feedJPEGQuality,
	}
}

// SetSyncCallback registers the function called on every synchronized tick.
// It is called from a single goroutine; do not block inside it.
func (m *Manager) SetSyncCallback(cb func([]SyncFrame)) { m.onSync = cb }

// AddCamera starts capturing from device and registers the camera under id.
// Returns an error if ffmpeg cannot be started.
func (m *Manager) AddCamera(ctx context.Context, id, device string, width, height, fps int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.cameras[id]; exists {
		return fmt.Errorf("camera %q already registered", id)
	}

	cam := &camera{id: id, device: device, width: width, height: height, fps: fps}
	if err := cam.start(ctx, m.motionAmplify, m.feedJPEGQuality); err != nil {
		return fmt.Errorf("camera %q: %w", id, err)
	}
	m.cameras[id] = cam
	log.Printf("capture: camera %q started (%s %dx%d@%dfps)", id, device, width, height, fps)
	return nil
}

// RemoveCamera stops the camera's ffmpeg process and removes it.
func (m *Manager) RemoveCamera(id string) {
	m.mu.Lock()
	cam, ok := m.cameras[id]
	if ok {
		delete(m.cameras, id)
	}
	m.mu.Unlock()
	if ok {
		cam.stop()
		log.Printf("capture: camera %q stopped", id)
	}
}

// LatestFrame returns the most recently captured frame for the given camera id,
// or nil if nothing has been captured yet.
func (m *Manager) LatestFrame(id string) *Frame {
	m.mu.RLock()
	cam, ok := m.cameras[id]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	cam.mu.RLock()
	defer cam.mu.RUnlock()
	return cam.latest
}

// CameraIDs returns a snapshot of currently active camera IDs.
func (m *Manager) CameraIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.cameras))
	for id := range m.cameras {
		ids = append(ids, id)
	}
	return ids
}

// RunSync runs the synchronized sampling loop until ctx is cancelled.
// This should be called in its own goroutine.
func (m *Manager) RunSync(ctx context.Context) {
	ticker := time.NewTicker(m.syncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.doSync()
		}
	}
}

// StopAll stops all active cameras.
func (m *Manager) StopAll() {
	m.mu.Lock()
	cams := make([]*camera, 0, len(m.cameras))
	for _, c := range m.cameras {
		cams = append(cams, c)
	}
	m.cameras = make(map[string]*camera)
	m.mu.Unlock()
	for _, c := range cams {
		c.stop()
	}
}

func (m *Manager) doSync() {
	if m.onSync == nil {
		return
	}
	m.mu.RLock()
	cams := make([]*camera, 0, len(m.cameras))
	for _, c := range m.cameras {
		cams = append(cams, c)
	}
	m.mu.RUnlock()

	// Snapshot all cameras atomically (as close as possible).
	frames := make([]SyncFrame, 0, len(cams))
	for _, c := range cams {
		c.mu.RLock()
		f := c.latest
		c.mu.RUnlock()
		if f != nil {
			frames = append(frames, SyncFrame{CameraID: c.id, Frame: f})
		}
	}
	if len(frames) > 0 {
		m.onSync(frames)
	}
}

// ── per-camera methods ─────────────────────────────────────────────────────────

func (c *camera) start(ctx context.Context, motionAmplify, jpegQuality int) error {
	camCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	args := buildFFmpegArgs(c.device, c.width, c.height, c.fps)
	cmd := exec.CommandContext(camCtx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start: %w", err)
	}
	c.cmd = cmd
	go c.readLoop(camCtx, stdout, motionAmplify, jpegQuality)
	return nil
}

func (c *camera) stop() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.cmd != nil {
		_ = c.cmd.Wait()
	}
}

// readLoop reads MJPEG frames from the ffmpeg pipe and updates c.latest.
// It also computes a grayscale motion-diff frame for visualization.
func (c *camera) readLoop(ctx context.Context, r io.Reader, motionAmplify, jpegQuality int) {
	br := bufio.NewReaderSize(r, 2<<20) // 2 MB
	var buf []byte
	inFrame := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		b, err := br.ReadByte()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("capture: camera %q read error: %v", c.id, err)
			}
			return
		}

		if !inFrame {
			if b == 0xFF {
				next, err := br.ReadByte()
				if err != nil {
					return
				}
				if next == 0xD8 {
					inFrame = true
					buf = append(buf[:0], 0xFF, 0xD8)
				}
			}
			continue
		}

		buf = append(buf, b)

		// Detect JPEG End Of Image marker
		if len(buf) >= 2 && buf[len(buf)-2] == 0xFF && buf[len(buf)-1] == 0xD9 {
			jpegData := make([]byte, len(buf))
			copy(jpegData, buf)

			now := time.Now()
			motionJPEG := c.computeMotionJPEG(jpegData, motionAmplify, jpegQuality)

			c.mu.Lock()
			c.latest = &Frame{
				JPEG:       jpegData,
				MotionJPEG: motionJPEG,
				CapturedAt: now,
				Width:      c.width,
				Height:     c.height,
			}
			c.mu.Unlock()

			buf = buf[:0]
			inFrame = false
		}
	}
}

// computeMotionJPEG decodes the JPEG, diffs it against the previous grayscale
// frame, amplifies the result, and returns a new JPEG of the diff image.
// Returns nil if this is the first frame.
func (c *camera) computeMotionJPEG(jpegData []byte, amplify, quality int) []byte {
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil
	}
	b := img.Bounds()
	w, h := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y

	gray := toGray(img, w, h)

	if c.prevGray == nil || len(c.prevGray) != len(gray) {
		c.prevGray = gray
		return nil
	}

	diff := image.NewGray(image.Rect(0, 0, w, h))
	for i := range gray {
		d := int(gray[i]) - int(c.prevGray[i])
		if d < 0 {
			d = -d
		}
		v := d * amplify
		if v > 255 {
			v = 255
		}
		diff.Pix[i] = uint8(v)
	}
	c.prevGray = gray

	var out bytes.Buffer
	if err := jpeg.Encode(&out, diff, &jpeg.Options{Quality: quality}); err != nil {
		return nil
	}
	return out.Bytes()
}

func toGray(img image.Image, w, h int) []uint8 {
	g := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, gr, bl, _ := img.At(x, y).RGBA()
			lum := (299*r + 587*gr + 114*bl) / 1000
			g[y*w+x] = uint8(lum >> 8)
		}
	}
	return g
}

// ── ffmpeg command builder ─────────────────────────────────────────────────────

// buildFFmpegArgs returns the ffmpeg argument list for the current OS.
// The output is an MJPEG stream written to stdout.
func buildFFmpegArgs(device string, width, height, fps int) []string {
	size := fmt.Sprintf("%dx%d", width, height)
	rate := fmt.Sprintf("%d", fps)

	common := []string{
		"-hide_banner", "-loglevel", "error",
		"-framerate", rate,
		"-video_size", size,
		"-i", device,
		"-f", "mjpeg", "-q:v", "5", "pipe:1",
	}

	switch runtime.GOOS {
	case "darwin":
		return append([]string{"-f", "avfoundation"}, common...)
	case "windows":
		return append([]string{"-f", "dshow"}, common...)
	default: // linux and others
		return append([]string{"-f", "v4l2"}, common...)
	}
}
