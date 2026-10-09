package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsWriteTimeout bounds how long one status message may take to reach a client.
const wsWriteTimeout = 2 * time.Second

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all local / LAN origins
	},
}

type WSManager struct {
	handler *APIHandler
	clients map[*websocket.Conn]bool
	mu      sync.Mutex
	stopCh  chan struct{}
}

func NewWSManager(handler *APIHandler) *WSManager {
	m := &WSManager{
		handler: handler,
		clients: make(map[*websocket.Conn]bool),
		stopCh:  make(chan struct{}),
	}
	go m.broadcastLoop()
	return m
}

func (m *WSManager) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS upgrade error: %v", err)
		return
	}

	m.mu.Lock()
	m.clients[conn] = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.clients, conn)
		m.mu.Unlock()
		conn.Close()
	}()

	// Keep connection alive reading messages or close errors
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (m *WSManager) broadcastLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			payload := map[string]interface{}{
				"pipeline":  m.handler.engine.GetStatus(),
				"metrics":   m.handler.streamers.GetAllMetrics(),
				"scene":     m.handler.engine.GetSceneStatus(),
				"timestamp": time.Now().UnixMilli(),
			}

			data, err := json.Marshal(payload)
			if err != nil {
				continue
			}

			// Write outside the lock, with a deadline: a stalled client must not block
			// the other clients, HandleWS, or Close.
			m.mu.Lock()
			conns := make([]*websocket.Conn, 0, len(m.clients))
			for conn := range m.clients {
				conns = append(conns, conn)
			}
			m.mu.Unlock()

			var failed []*websocket.Conn
			for _, conn := range conns {
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					failed = append(failed, conn)
				}
			}

			if len(failed) > 0 {
				m.mu.Lock()
				for _, conn := range failed {
					conn.Close()
					delete(m.clients, conn)
				}
				m.mu.Unlock()
			}
		}
	}
}

func (m *WSManager) Close() {
	close(m.stopCh)
	m.mu.Lock()
	defer m.mu.Unlock()
	for conn := range m.clients {
		conn.Close()
	}
	m.clients = make(map[*websocket.Conn]bool)
}
