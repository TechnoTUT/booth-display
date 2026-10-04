package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

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
				"timestamp": time.Now().UnixMilli(),
			}

			data, err := json.Marshal(payload)
			if err != nil {
				continue
			}

			m.mu.Lock()
			for conn := range m.clients {
				err := conn.WriteMessage(websocket.TextMessage, data)
				if err != nil {
					conn.Close()
					delete(m.clients, conn)
				}
			}
			m.mu.Unlock()
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
