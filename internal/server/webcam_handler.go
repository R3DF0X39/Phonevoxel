package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/phonevoxel/internal/capture"
	"github.com/phonevoxel/internal/geo"
	"github.com/phonevoxel/internal/types"
)

// webcamState holds the runtime state of the webcam subsystem.
// It is owned by the Server and initialised in New().
type webcamState struct {
	mu         sync.RWMutex
	cameras    []types.WebcamCameraEntry
	statePath  string
	mgr        *capture.Manager
	motionBufs sync.Map // cameraID → []byte (latest motion JPEG)
}

// loadWebcamState reads the persisted camera list from disk (best-effort).
func loadWebcamState(statePath string) []types.WebcamCameraEntry {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	var sf types.WebcamStateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		log.Printf("webcam: failed to parse %s: %v", statePath, err)
		return nil
	}
	return sf.Cameras
}

// saveWebcamState persists the camera list atomically.
func (ws *webcamState) save() {
	ws.mu.RLock()
	sf := types.WebcamStateFile{Cameras: ws.cameras}
	ws.mu.RUnlock()

	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		log.Printf("webcam: marshal error: %v", err)
		return
	}
	tmp := ws.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("webcam: write error: %v", err)
		return
	}
	_ = os.Rename(tmp, ws.statePath)
}

// startAllCameras spawns ffmpeg for every persisted camera on startup.
func (ws *webcamState) startAllCameras(ctx context.Context) {
	ws.mu.RLock()
	cams := make([]types.WebcamCameraEntry, len(ws.cameras))
	copy(cams, ws.cameras)
	ws.mu.RUnlock()

	for _, cam := range cams {
		if err := ws.mgr.AddCamera(ctx, cam.ID, cam.Device, cam.Width, cam.Height, cam.FPS); err != nil {
			log.Printf("webcam: could not start camera %q: %v", cam.ID, err)
		}
	}
}

// ── HTTP handlers ──────────────────────────────────────────────────────────────

// handleWebcamCameras serves GET/POST/PUT/DELETE /api/webcam/cameras[/{id}].
func (s *Server) handleWebcamCameras(w http.ResponseWriter, r *http.Request) {
	// Extract optional trailing /{id}
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/webcam/cameras")
	trimmed = strings.TrimPrefix(trimmed, "/")
	id := trimmed // empty string → collection endpoint

	switch r.Method {
	case http.MethodGet:
		s.webcam.mu.RLock()
		cams := s.webcam.cameras
		s.webcam.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cams) //nolint:errcheck

	case http.MethodPost:
		var cam types.WebcamCameraEntry
		if err := json.NewDecoder(r.Body).Decode(&cam); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if cam.ID == "" {
			cam.ID = fmt.Sprintf("cam-%d", time.Now().UnixNano())
		}
		if cam.Width == 0 {
			cam.Width = 640
		}
		if cam.Height == 0 {
			cam.Height = 480
		}
		if cam.FPS == 0 {
			cam.FPS = 30
		}
		if cam.HFOV == 0 {
			cam.HFOV = 70
		}

		// Start ffmpeg capture
		if err := s.webcam.mgr.AddCamera(r.Context(), cam.ID, cam.Device, cam.Width, cam.Height, cam.FPS); err != nil {
			http.Error(w, fmt.Sprintf("failed to start camera: %v", err), http.StatusInternalServerError)
			return
		}

		// Register in pipeline so voxel accumulation can start
		s.registerWebcamInPipeline(cam)

		s.webcam.mu.Lock()
		s.webcam.cameras = append(s.webcam.cameras, cam)
		s.webcam.mu.Unlock()
		s.webcam.save()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(cam) //nolint:errcheck

	case http.MethodPut:
		if id == "" {
			http.Error(w, "PUT requires /api/webcam/cameras/{id}", http.StatusBadRequest)
			return
		}
		var updated types.WebcamCameraEntry
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		updated.ID = id

		s.webcam.mu.Lock()
		found := false
		for i, c := range s.webcam.cameras {
			if c.ID == id {
				s.webcam.cameras[i] = updated
				found = true
				break
			}
		}
		s.webcam.mu.Unlock()

		if !found {
			http.Error(w, "camera not found", http.StatusNotFound)
			return
		}

		// Restart ffmpeg with new device/resolution if changed
		s.webcam.mgr.RemoveCamera(id)
		s.pipeline.UnregisterClient(id)
		if err := s.webcam.mgr.AddCamera(r.Context(), updated.ID, updated.Device, updated.Width, updated.Height, updated.FPS); err != nil {
			log.Printf("webcam: restart camera %q: %v", id, err)
		}
		s.registerWebcamInPipeline(updated)

		s.webcam.save()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(updated) //nolint:errcheck

	case http.MethodDelete:
		if id == "" {
			http.Error(w, "DELETE requires /api/webcam/cameras/{id}", http.StatusBadRequest)
			return
		}
		s.webcam.mgr.RemoveCamera(id)
		s.pipeline.UnregisterClient(id)

		s.webcam.mu.Lock()
		cameras := s.webcam.cameras[:0]
		for _, c := range s.webcam.cameras {
			if c.ID != id {
				cameras = append(cameras, c)
			}
		}
		s.webcam.cameras = cameras
		s.webcam.mu.Unlock()
		s.webcam.save()
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWebcamFeed streams the raw MJPEG feed for one camera.
// URL: /webcam/feed/{id}
func (s *Server) handleWebcamFeed(w http.ResponseWriter, r *http.Request) {
	id := path.Base(r.URL.Path)
	s.streamMJPEG(w, r, id, false)
}

// handleWebcamMotion streams the motion-diff MJPEG feed for one camera.
// URL: /webcam/motion/{id}
func (s *Server) handleWebcamMotion(w http.ResponseWriter, r *http.Request) {
	id := path.Base(r.URL.Path)
	s.streamMJPEG(w, r, id, true)
}

// streamMJPEG writes a multipart/x-mixed-replace MJPEG stream to the response.
func (s *Server) streamMJPEG(w http.ResponseWriter, r *http.Request, cameraID string, motion bool) {
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(33 * time.Millisecond) // ~30 fps max
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			frame := s.webcam.mgr.LatestFrame(cameraID)
			if frame == nil {
				continue
			}
			var data []byte
			if motion {
				data = frame.MotionJPEG
			} else {
				data = frame.JPEG
			}
			if len(data) == 0 {
				continue
			}
			fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(data))
			w.Write(data) //nolint:errcheck
			fmt.Fprintf(w, "\r\n")
			flusher.Flush()
		}
	}
}

// handleWebcamOrigin sets the ENU origin from a lat/lon provided by the frontend
// (e.g. when the user selects the ground-plane bounding box on the Leaflet map).
// POST /api/webcam/origin  { "lat": ..., "lon": ..., "alt": ... }
func (s *Server) handleWebcamOrigin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
		Alt float64 `json:"alt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	s.geoConv.SetOrigin(req.Lat, req.Lon, req.Alt)
	log.Printf("webcam: ENU origin set to %.6f, %.6f, %.2f", req.Lat, req.Lon, req.Alt)
	w.WriteHeader(http.StatusNoContent)
}

// handleTileProxy fetches a map tile on behalf of the browser to avoid CORS issues.
// GET /api/tile-proxy?url=https://...
func (s *Server) handleTileProxy(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}

	// Basic allowlist: only proxy known satellite tile servers.
	parsed, err := url.Parse(rawURL)
	if err != nil || !isTileHostAllowed(parsed.Host) {
		http.Error(w, "tile host not allowed", http.StatusForbidden)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		http.Error(w, "tile fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	io.Copy(w, resp.Body) //nolint:errcheck
}

// isTileHostAllowed returns true for known, safe satellite tile providers.
func isTileHostAllowed(host string) bool {
	allowed := []string{
		"server.arcgisonline.com",
		"tile.openstreetmap.org",
		"a.tile.openstreetmap.org",
		"b.tile.openstreetmap.org",
		"c.tile.openstreetmap.org",
		"mt0.google.com",
		"mt1.google.com",
		"mt2.google.com",
		"mt3.google.com",
		"khms0.google.com",
		"khms1.google.com",
		"services.arcgisonline.com",
	}
	for _, a := range allowed {
		if host == a {
			return true
		}
	}
	return false
}

// ── pipeline integration ───────────────────────────────────────────────────────

// registerWebcamInPipeline registers a webcam as a pipeline client and
// immediately submits a dummy zero frame so the client appears in the status list.
func (s *Server) registerWebcamInPipeline(cam types.WebcamCameraEntry) {
	ctx := context.Background()
	_ = s.pipeline.RegisterClient(ctx, cam.ID)
}

// webcamSyncCallback is called by the capture.Manager on every sync tick.
// It translates each frame into a ClientFrame and submits it to the pipeline.
func (s *Server) webcamSyncCallback(frames []capture.SyncFrame) {
	for _, sf := range frames {
		cam := s.findWebcamCamera(sf.Frame, sf.CameraID)
		if cam == nil {
			continue
		}
		if len(sf.Frame.JPEG) == 0 {
			continue
		}

		cf := &types.ClientFrame{
			ClientID:        sf.CameraID,
			ClientTimestamp: float64(sf.Frame.CapturedAt.UnixMilli()) / 1000.0,
			ServerTimestamp: sf.Frame.CapturedAt,
			Lat:             cam.Lat,
			Lon:             cam.Lon,
			Alt:             cam.AltMeters,
			GPSAccuracy:     0.1, // fixed mount → near-perfect position knowledge
			// Alpha/Beta/Gamma unused; pipeline uses them only if ComputePose is called.
			// We override via the pose set on the client directly below.
			Alpha:       cam.Azimuth,
			Beta:        90 - cam.Elevation, // W3C-compatible mapping for display
			Gamma:       cam.Roll,
			HFOV:        cam.HFOV,
			FrameWidth:  uint32(cam.Width),
			FrameHeight: uint32(cam.Height),
			JPEG:        sf.Frame.JPEG,
			// Signal to pipeline to use azimuth/elevation rather than W3C orientation.
			UseAzimuthElevation: true,
			Elevation:           cam.Elevation,
		}

		c := s.pipeline.GetClient(sf.CameraID)
		if c != nil {
			select {
			case c.Inbox <- cf:
			default:
				// Drop frame if pipeline is backed up — that's fine at 30 Hz.
			}
		}
	}
}

// findWebcamCamera looks up the configuration for the given camera ID.
func (s *Server) findWebcamCamera(_ *capture.Frame, id string) *types.WebcamCameraEntry {
	s.webcam.mu.RLock()
	defer s.webcam.mu.RUnlock()
	for i := range s.webcam.cameras {
		if s.webcam.cameras[i].ID == id {
			return &s.webcam.cameras[i]
		}
	}
	return nil
}

// geocamPose returns the ENU CameraPose for a webcam camera, using azimuth/elevation/roll.
func (s *Server) geocamPose(cam *types.WebcamCameraEntry) geo.CameraPose {
	return geo.ComputePoseFromAzimuthElevation(
		s.geoConv,
		cam.Lat, cam.Lon, cam.AltMeters,
		cam.Azimuth, cam.Elevation, cam.Roll,
	)
}
