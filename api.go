package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ==========================================
// REST API FOR DASHBOARD
// ==========================================

func registerAPIRoutes() {
	http.HandleFunc("/api/status", handleGetStatus)
	http.HandleFunc("/api/connections", handleConnections)
	http.HandleFunc("/api/connections/", handleConnectionByKey)
	http.HandleFunc("/api/command", handleCommand)
	http.HandleFunc("/api/ws/status", handleWSStatus)
}

// --- GET /api/status ---
func handleGetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := map[string]interface{}{
		"gorilla": map[string]interface{}{
			"connections":   server.GetConnectionsInfo(),
			"total_clients": server.ClientCount(),
		},
		"centrifugo":      cfManager.GetStatus(),
		"tick_multiplier": GetGlobalRateMultiplier(),
		"timestamp":       time.Now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(status)
}

// --- GET/POST /api/connections ---
func handleConnections(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.Method {
	case http.MethodGet:
		conns := mockDB.ListConnections()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(conns)

	case http.MethodPost:
		var req ConnectionConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.ID == "" {
			http.Error(w, "ID is required", http.StatusBadRequest)
			return
		}

		err := mockDB.SetConnection(req.ID, req)
		if err != nil {
			http.Error(w, "Save error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "created", "id": req.ID})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- GET/PUT/DELETE /api/connections/{id} ---
func handleConnectionByKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/connections/")
	if id == "" {
		http.Error(w, "ID is required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		m, ok := mockDB.GetConnection(id)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(m)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Read error", http.StatusBadRequest)
			return
		}

		var req ConnectionConfig
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Ensure ID matches path
		req.ID = id
		err = mockDB.SetConnection(id, req)
		if err != nil {
			http.Error(w, "Save error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "updated", "id": id})

	case http.MethodDelete:
		err := mockDB.DeleteConnection(id)
		if err != nil {
			http.Error(w, "Delete error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": id})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- POST /api/command ---
type CommandRequest struct {
	Target  string `json:"target"`
	Command string `json:"command"`
	Value   string `json:"value"`
}

func handleCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var cmd CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	result := map[string]interface{}{"status": "ok"}

	switch cmd.Command {
	case "close_all":
		if cmd.Target == "centrifugo" {
			cfManager.DisconnectAll()
			result["message"] = "All Centrifugo clients disconnected"
		} else {
			go server.CloseAll(nil)
			result["message"] = "All Gorilla connections closed"
		}

	case "close_connection":
		if cmd.Target == "gorilla" && cmd.Value != "" {
			server.CloseConnection(cmd.Value)
			result["message"] = fmt.Sprintf("Gorilla connection %s closed", cmd.Value)
		}

	case "disconnect_channel":
		if cmd.Target == "centrifugo" && cmd.Value != "" {
			cfManager.DisconnectChannel(cmd.Value)
			result["message"] = fmt.Sprintf("Centrifugo channel %s disconnected", cmd.Value)
		}

	case "disconnect_public":
		cfManager.DisconnectType("public")
		result["message"] = "All public Centrifugo channels disconnected"

	case "disconnect_private":
		cfManager.DisconnectType("private")
		result["message"] = "All private Centrifugo channels disconnected"

	case "simulate_peer_closed":
		if cmd.Target == "centrifugo" {
			cfManager.DisconnectAll()
			result["message"] = "Centrifugo peer closed simulated"
		} else {
			go server.BroadcastPeerClosed(nil)
			result["message"] = "Gorilla peer closed broadcast sent"
		}

	case "set_tick_multiplier":
		val, err := strconv.ParseFloat(cmd.Value, 64)
		if err != nil || val <= 0 {
			http.Error(w, "Invalid multiplier value", http.StatusBadRequest)
			return
		}
		SetGlobalRateMultiplier(val)
		result["message"] = fmt.Sprintf("Global tick rate multiplier set to %gx", val)

	default:
		http.Error(w, "Unknown command: "+cmd.Command, http.StatusBadRequest)
		return
	}

	json.NewEncoder(w).Encode(result)
}

// --- WebSocket /api/ws/status (real-time status push) ---
var wsStatusUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleWSStatus(w http.ResponseWriter, r *http.Request) {
	conn, err := wsStatusUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[API] WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	log.Println("[API] Dashboard WebSocket connected")

	var writeMu sync.Mutex

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			log.Println("[API] Dashboard WebSocket disconnected")
			return
		case <-ticker.C:
			status := map[string]interface{}{
				"gorilla": map[string]interface{}{
					"connections":   server.GetConnectionsInfo(),
					"total_clients": server.ClientCount(),
				},
				"centrifugo":      cfManager.GetStatus(),
				"tick_multiplier": GetGlobalRateMultiplier(),
				"timestamp":       time.Now().Format(time.RFC3339),
			}

			writeMu.Lock()
			err := conn.WriteJSON(status)
			writeMu.Unlock()
			if err != nil {
				log.Printf("[API] Dashboard WebSocket write error: %v", err)
				return
			}
		}
	}
}
