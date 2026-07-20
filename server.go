package main

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ==========================================
// GORILLA WEBSOCKET (LEGACY) STATE
// ==========================================

// SubContext holds the context for a specific subscription's periodic events
type SubContext struct {
	ConfigID  string
	EngineCtx EngineContext
	Events    []PeriodicEvent
}

// ClientConn represents a connected Gorilla WebSocket client with monitoring metadata.
type ClientConn struct {
	conn       *websocket.Conn
	cancel     context.CancelFunc
	writeMu    sync.Mutex
	ConnID     string
	ConfigID   string
	Endpoint   string
	ConnectAt  time.Time
	SubsMu     sync.RWMutex
	ActiveSubs map[string]SubContext // key is usually a unique subscription identifier
}

// ConnectionInfo is a JSON-serializable snapshot of a ClientConn for the dashboard API.
type ConnectionInfo struct {
	ConnID     string   `json:"conn_id"`
	ConfigID   string   `json:"config_id"`
	Endpoint   string   `json:"endpoint"`
	ConnectAt  string   `json:"connect_at"`
	Duration   string   `json:"duration"`
	ActiveSubs []string `json:"active_subs"`
	SubCount   int      `json:"sub_count"`
}

// MockServer holds global state for the legacy Gorilla WebSocket endpoints.
type MockServer struct {
	clients   map[*ClientConn]bool
	clientsMu sync.Mutex
}

func NewMockServer() *MockServer {
	return &MockServer{
		clients: make(map[*ClientConn]bool),
	}
}

func (s *MockServer) AddClient(c *ClientConn) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	s.clients[c] = true
}

func (s *MockServer) RemoveClient(c *ClientConn) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	delete(s.clients, c)
}

func (s *MockServer) CloseAll(sender *ClientConn) {
	s.clientsMu.Lock()
	clientsCopy := make([]*ClientConn, 0, len(s.clients))
	for c := range s.clients {
		if c != sender {
			clientsCopy = append(clientsCopy, c)
		}
	}
	s.clientsMu.Unlock()

	log.Printf("Abruptly closing %d OTHER active connection(s)...", len(clientsCopy))
	for _, c := range clientsCopy {
		c.cancel() // This cancels the client connection context
		c.conn.Close()
	}
}

func (s *MockServer) BroadcastPeerClosed(sender *ClientConn) {
	s.clientsMu.Lock()
	clientsCopy := make([]*ClientConn, 0, len(s.clients))
	for c := range s.clients {
		if c != sender {
			clientsCopy = append(clientsCopy, c)
		}
	}
	s.clientsMu.Unlock()

	log.Printf("Broadcasting graceful peer closed to %d OTHER client(s)...", len(clientsCopy))
	closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Server requested graceful close")

	for _, c := range clientsCopy {
		go func(client *ClientConn) {
			client.writeMu.Lock()
			client.conn.WriteMessage(websocket.CloseMessage, closeMsg)
			client.writeMu.Unlock()
			time.Sleep(100 * time.Millisecond)
			client.cancel()
			client.conn.Close()
		}(c)
	}
}

// GetConnectionsInfo returns a snapshot of all active Gorilla connections for the dashboard.
func (s *MockServer) GetConnectionsInfo() []ConnectionInfo {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	infos := make([]ConnectionInfo, 0, len(s.clients))
	for c := range s.clients {
		c.SubsMu.RLock()
		subs := make([]string, 0, len(c.ActiveSubs))
		for sub := range c.ActiveSubs {
			subs = append(subs, sub)
		}
		c.SubsMu.RUnlock()

		infos = append(infos, ConnectionInfo{
			ConnID:     c.ConnID,
			ConfigID:   c.ConfigID,
			Endpoint:   c.Endpoint,
			ConnectAt:  c.ConnectAt.Format(time.RFC3339),
			Duration:   time.Since(c.ConnectAt).Round(time.Second).String(),
			ActiveSubs: subs,
			SubCount:   len(subs),
		})
	}
	return infos
}

func (s *MockServer) ClientCount() int {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	return len(s.clients)
}

func (s *MockServer) CloseConnection(connID string) {
	s.clientsMu.Lock()
	var target *ClientConn
	for c := range s.clients {
		if c.ConnID == connID {
			target = c
			break
		}
	}
	s.clientsMu.Unlock()

	if target != nil {
		log.Printf("[GORILLA] Manually closing connection ID: %s", connID)
		target.cancel()
		target.conn.Close()
	}
}

var server *MockServer

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
