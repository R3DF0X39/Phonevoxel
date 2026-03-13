package server

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// handleClient upgrades the connection and streams frames to the pipeline.
func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS upgrade error: %v", err)
		return
	}
	defer conn.Close()

	clientID := generateClientID()
	log.Printf("Client %s connected from %s", clientID, r.RemoteAddr)

	ctx := r.Context()
	client := s.pipeline.RegisterClient(ctx, clientID)
	defer s.pipeline.UnregisterClient(clientID)

	// Send a welcome message so the client knows their assigned ID
	if err := conn.WriteJSON(map[string]string{"type": "welcome", "client_id": clientID}); err != nil {
		log.Printf("Client %s: welcome write error: %v", clientID, err)
		return
	}

	// Set a read deadline that we refresh on each message
	const idleTimeout = 30 * time.Second

	for {
		conn.SetReadDeadline(time.Now().Add(idleTimeout)) //nolint:errcheck
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure) {
				log.Printf("Client %s: read error: %v", clientID, err)
			}
			break
		}
		if msgType != websocket.BinaryMessage {
			continue
		}

		frame, err := parseClientMessage(data)
		if err != nil {
			log.Printf("Client %s: parse error: %v", clientID, err)
			continue
		}
		frame.ClientID = clientID
		frame.ServerTimestamp = time.Now()

		// Non-blocking send; drop frame if pipeline is backed up
		select {
		case client.Inbox <- frame:
		default:
		}
	}

	log.Printf("Client %s disconnected", clientID)
}

// generateClientID creates a short random-ish identifier.
func generateClientID() string {
	return fmt.Sprintf("c%d", time.Now().UnixNano()&0xFFFFFF)
}
