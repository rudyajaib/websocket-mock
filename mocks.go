package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// ==========================================
// MOCK DATABASE (JSON PERSISTENCE)
// ==========================================

// RandomizerRule defines how a specific field should be generated/mutated.
type RandomizerRule struct {
	Type       string  `json:"type"`       // "int", "double", "double_string", "random_string", "static_string"
	Min        float64 `json:"min"`        // Minimum value (if not using percentage)
	Max        float64 `json:"max"`        // Maximum value (if not using percentage)
	Percentage float64 `json:"percentage"` // Percentage variance (+- %) for random walk
	StaticVal  string  `json:"static_val"` // Static string replacement
}

// RequestResponsePair defines a client request and the expected server response
type RequestResponsePair struct {
	Description      string          `json:"description"`
	SubKey           string          `json:"sub_key,omitempty"` // template string for generating a subscription key (e.g. {{.asset_id}})
	RequestTemplate  json.RawMessage `json:"request_template"`  // what client sends
	ResponseTemplate json.RawMessage `json:"response_template"` // what server replies
}

// PingConfig for ping/pong
type PingConfig struct {
	PingMessage string `json:"ping_message"` // e.g. "Ping"
	PongMessage string `json:"pong_message"` // e.g. "Pong"
	IsJSON      bool   `json:"is_json"`      // whether ping/pong are JSON format
}

// PeriodicEvent defines a server event sent at intervals
type PeriodicEvent struct {
	ID          string                    `json:"id"`
	Description string                    `json:"description"`
	RateMs      int64                     `json:"rate_ms"`     // interval in milliseconds (0 means disabled)
	Template    json.RawMessage           `json:"template"`    // response JSON template
	Randomizers map[string]RandomizerRule `json:"randomizers"` // per-field randomizers
}

// ConnectionConfig represents a full channel/connection configuration
type ConnectionConfig struct {
	ID          string `json:"id"`          // unique ID
	Description string `json:"description"`
	Category    string `json:"category"`    // "gorilla" or "centrifugo"

	// Gorilla-specific
	Path string `json:"path"` // e.g. "/ws/v3/coin-data/price"

	// Centrifugo-specific
	ChannelName     string                `json:"channel_name,omitempty"`
	ChannelType     string                `json:"channel_type,omitempty"`
	MonitorGroupVar string                `json:"monitor_group_var,omitempty"`
	SubscribeRequests   []RequestResponsePair `json:"subscribe_requests"`
	UnsubscribeRequests []RequestResponsePair `json:"unsubscribe_requests"`
	PingRequest         *PingConfig           `json:"ping_request"` // optional

	// Periodic server events
	PeriodicEvents []PeriodicEvent `json:"periodic_events"`

	Enabled bool `json:"enabled"`
}

// MockDB manages the connection configs with JSON file persistence.
type MockDB struct {
	Connections map[string]ConnectionConfig `json:"connections"`
	mu          sync.RWMutex
	filePath    string
}

func NewMockDB(filePath string) *MockDB {
	return &MockDB{
		Connections: make(map[string]ConnectionConfig),
		filePath:    filePath,
	}
}

// Load reads the mock database from disk; creates defaults if file doesn't exist.
func (db *MockDB) Load() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	data, err := os.ReadFile(db.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[MOCKS] %s not found, generating defaults...", db.filePath)
			db.generateDefaults()
			return db.saveUnsafe()
		}
		return err
	}

	// Just for backward compatibility, if the old format is loaded, we clear and generate defaults.
	// We're dropping support for old flat `mocks` map per user's request.
	var fileData struct {
		Connections map[string]ConnectionConfig `json:"connections"`
	}

	if err := json.Unmarshal(data, &fileData); err != nil || len(fileData.Connections) == 0 {
		log.Printf("[MOCKS] No valid connections found in %s, generating defaults...", db.filePath)
		db.generateDefaults()
		return db.saveUnsafe()
	}

	db.Connections = fileData.Connections

	log.Printf("[MOCKS] Loaded %d connection configs from %s", len(db.Connections), db.filePath)
	return nil
}

// Save persists the current state to disk.
func (db *MockDB) Save() error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.saveUnsafe()
}

func (db *MockDB) saveUnsafe() error {
	fileData := struct {
		Connections map[string]ConnectionConfig `json:"connections"`
	}{
		Connections: db.Connections,
	}
	data, err := json.MarshalIndent(fileData, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(db.filePath, data, 0644)
}

// ListConnections returns all connection configs.
func (db *MockDB) ListConnections() map[string]ConnectionConfig {
	db.mu.RLock()
	defer db.mu.RUnlock()

	result := make(map[string]ConnectionConfig, len(db.Connections))
	for k, v := range db.Connections {
		result[k] = v
	}
	return result
}

// GetConnection returns a single connection config by key.
func (db *MockDB) GetConnection(key string) (ConnectionConfig, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	m, ok := db.Connections[key]
	return m, ok
}

// SetConnection creates or updates a connection config.
func (db *MockDB) SetConnection(key string, c ConnectionConfig) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.Connections[key] = c
	return db.saveUnsafe()
}

// DeleteConnection removes a connection config.
func (db *MockDB) DeleteConnection(key string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	delete(db.Connections, key)
	return db.saveUnsafe()
}

func (db *MockDB) generateDefaults() {
	db.Connections = map[string]ConnectionConfig{}
}

func marshalJSON(v interface{}) json.RawMessage {
	data, _ := json.MarshalIndent(v, "", "  ")
	return data
}

var mockDB *MockDB
