package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/phonevoxel/internal/config"
	"github.com/phonevoxel/internal/geo"
	"github.com/phonevoxel/internal/pipeline"
	"github.com/phonevoxel/internal/types"
	"github.com/phonevoxel/internal/voxel"
)

// rewriteRequest clones r with a different URL path so http.FileServer resolves
// the correct embedded file (e.g. /client → /client.html).
func rewriteRequest(r *http.Request, path string) *http.Request {
	r2 := r.Clone(r.Context())
	r2.URL = r.URL
	url2 := *r.URL
	url2.Path = path
	r2.URL = &url2
	return r2
}

// Server is the main HTTP/WebSocket server.
type Server struct {
	cfg      config.Config
	pipeline *pipeline.Pipeline
	grid     *voxel.Grid
	geoConv  *geo.Converter
	upgrader websocket.Upgrader
	dashHub  *dashboardHub
	mux      *http.ServeMux
}

// New constructs a Server wired to the given pipeline and grid.
func New(cfg config.Config, pl *pipeline.Pipeline, g *voxel.Grid, conv *geo.Converter, staticFS http.FileSystem) *Server {
	s := &Server{
		cfg:     cfg,
		pipeline: pl,
		grid:    g,
		geoConv: conv,
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(r *http.Request) bool { return true },
			ReadBufferSize:  1 << 16, // 64KB
			WriteBufferSize: 1 << 16,
		},
		dashHub: newDashboardHub(),
		mux:     http.NewServeMux(),
	}

	s.mux.HandleFunc("/ws/client", s.handleClient)
	s.mux.HandleFunc("/ws/dashboard", s.handleDashboard)
	s.mux.HandleFunc("/api/config", s.handleAPIConfig)
	s.mux.HandleFunc("/api/status", s.handleAPIStatus)
	s.mux.HandleFunc("/api/query-ray", s.handleQueryRay)
	s.mux.HandleFunc("/api/reset-grid", s.handleResetGrid)

	// Static files — served from the embedded FS
	fileServer := http.FileServer(staticFS)
	s.mux.Handle("/static/", http.StripPrefix("/static/", fileServer))

	// Page routes — serve named HTML files from the embedded FS
	s.mux.HandleFunc("/client",    func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(staticFS).ServeHTTP(w, rewriteRequest(r, "/client.html"))
	})
	s.mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(staticFS).ServeHTTP(w, rewriteRequest(r, "/dashboard.html"))
	})
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/client", http.StatusFound)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	return s
}

// ListenAndServeTLS starts the HTTPS server and background goroutines.
func (s *Server) ListenAndServeTLS(ctx context.Context) error {
	// Start dashboard broadcast loop
	go s.runDashboardBroadcast()

	srv := &http.Server{
		Addr:         s.cfg.Server.Addr,
		Handler:      s.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.Printf("PhoneVoxel server listening on %s (TLS)", s.cfg.Server.Addr)
	return srv.ListenAndServeTLS(s.cfg.Server.CertFile, s.cfg.Server.KeyFile)
}

// -- API handlers --

func (s *Server) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.cfg) //nolint:errcheck
	case http.MethodPost:
		var req types.ConfigUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.MotionThreshold != nil {
			s.pipeline.SetMotionThreshold(*req.MotionThreshold)
		}
		if req.DetectionThreshold != nil {
			s.pipeline.SetDetectionThreshold(*req.DetectionThreshold)
		}
		if req.DecayFactor != nil {
			s.grid.SetDecayFactor(*req.DecayFactor)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	type status struct {
		Clients  []types.ClientStatus `json:"clients"`
		GridMeta types.GridMeta       `json:"grid_meta"`
		OriginSet bool                `json:"origin_set"`
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status{ //nolint:errcheck
		Clients:   s.pipeline.ClientStatuses(),
		GridMeta:  s.grid.GridMeta(),
		OriginSet: s.geoConv.IsSet(),
	})
}

func (s *Server) handleQueryRay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req types.RayQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	pose, ok := s.pipeline.ClientPose(req.ClientID)
	if !ok {
		http.Error(w, "client not found", http.StatusNotFound)
		return
	}

	// We don't know the exact intrinsics without the last frame, so use a
	// reasonable default (70° HFOV at 640×480).
	intr := geo.IntrinsicsFromHFOV(640, 480, 70.0)
	dir := geo.PixelToRayENU(req.PixelX, req.PixelY, intr, pose)
	ray := voxel.NewRay(pose.East, pose.North, pose.Up, dir[0], dir[1], dir[2])

	posE, posN, posU, val := s.grid.QueryRay(ray)
	lat, lon, _ := s.geoConv.ENUToGPS(posE, posN, posU)

	resp := types.RayQueryResponse{
		Position: [3]float64{posE, posN, posU},
		LatLon:   [2]float64{lat, lon},
		Value:    val,
		Found:    val > 0,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (s *Server) handleResetGrid(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.grid.Reset()
	w.WriteHeader(http.StatusNoContent)
}
