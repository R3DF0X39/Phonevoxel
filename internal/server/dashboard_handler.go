package server

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/phonevoxel/internal/types"
)

// dashboardHub broadcasts DashboardUpdate messages to all connected dashboards.
type dashboardHub struct {
	mu      sync.RWMutex
	conns   map[*websocket.Conn]struct{}
}

func newDashboardHub() *dashboardHub {
	return &dashboardHub{conns: make(map[*websocket.Conn]struct{})}
}

func (h *dashboardHub) add(c *websocket.Conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *dashboardHub) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

func (h *dashboardHub) broadcast(msg interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.conns {
		c.SetWriteDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
		if err := c.WriteJSON(msg); err != nil {
			log.Printf("dashboard broadcast write error: %v", err)
		}
	}
}

// handleDashboard upgrades the connection and adds it to the hub.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("dashboard WS upgrade error: %v", err)
		return
	}
	defer conn.Close()

	s.dashHub.add(conn)
	defer s.dashHub.remove(conn)

	log.Printf("Dashboard viewer connected from %s", r.RemoteAddr)

	// Send an immediate snapshot so the dashboard doesn't wait for the first tick
	s.sendDashboardUpdate()

	// Keep the connection alive; read any pings / config update messages
	conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck
		return nil
	})

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if msgType == websocket.TextMessage {
			s.handleDashboardMessage(data)
		}
	}

	log.Printf("Dashboard viewer disconnected from %s", r.RemoteAddr)
}

// runDashboardBroadcast ticks at the configured rate and broadcasts grid snapshots.
func (s *Server) runDashboardBroadcast() {
	interval := time.Duration(float64(time.Second) / s.cfg.Dashboard.UpdateRateHz)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		s.sendDashboardUpdate()
	}
}

func (s *Server) sendDashboardUpdate() {
	sparse := s.grid.SparseSnapshot(s.cfg.Dashboard.SparseThreshold)
	targets := s.grid.DetectTargets(s.pipeline.DetectionThreshold())
	clients := s.pipeline.ClientStatuses()

	// Annotate targets with GPS lat/lon
	for i := range targets {
		lat, lon, _ := s.geoConv.ENUToGPS(
			targets[i].Position[0],
			targets[i].Position[1],
			targets[i].Position[2],
		)
		targets[i].LatLon = [2]float64{lat, lon}
	}

	update := types.DashboardUpdate{
		Timestamp:    float64(time.Now().UnixMilli()) / 1000.0,
		Clients:      clients,
		Targets:      targets,
		SparseVoxels: sparse,
		GridMeta:     s.grid.GridMeta(),
	}
	s.dashHub.broadcast(update)
}

// handleDashboardMessage processes control messages from dashboard clients
// (e.g. config updates sent as JSON text frames).
func (s *Server) handleDashboardMessage(data []byte) {
	// Future: parse config update requests from the dashboard
	log.Printf("dashboard message: %s", string(data))
}
