package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"
)

// ==========================================
// CENTRIFUGE MANAGER
// ==========================================

// CentrifugoChannelInfo represents a tracked Centrifuge channel for the dashboard.
type CentrifugoChannelInfo struct {
	Channel  string `json:"channel"`
	Type     string `json:"type"` // "public" or "private"
	SubCount int    `json:"sub_count"`
}

// CentrifugoClientInfo represents a connected Centrifuge client for the dashboard.
type CentrifugoClientInfo struct {
	ClientID       string   `json:"client_id"`
	UserID         string   `json:"user_id"`
	ConnectAt      string   `json:"connect_at"`
	Duration       string   `json:"duration"`
	Channels       []string `json:"channels"`
	ConnectionType string   `json:"connection_type"`
}

// CentrifugoStatus aggregates all centrifuge state for the dashboard.
type CentrifugoStatus struct {
	TotalClients  int                     `json:"total_clients"`
	TotalChannels int                     `json:"total_channels"`
	Clients       []CentrifugoClientInfo  `json:"clients"`
	Channels      []CentrifugoChannelInfo `json:"channels"`
	AjaibID       string                  `json:"ajaib_id"`
}

// CentrifugoManager manages the Centrifuge node and tracks state for the dashboard.
type CentrifugoManager struct {
	node      *centrifuge.Node
	ajaibID   string
	ajaibIDMu sync.RWMutex

	// Track connected clients
	clients   map[string]*cfClientState // keyed by client ID
	clientsMu sync.RWMutex

	// Track channel subscriptions
	channelSubs   map[string]map[string]bool // channel -> set of client IDs
	channelSubsMu sync.RWMutex

	// Track publisher contexts per channel
	publishers map[string]context.CancelFunc
}

type cfClientState struct {
	clientID  string
	userID    string
	connectAt time.Time
	channels  map[string]bool
	connType  string // "public" or "private"
}

func NewCentrifugoManager(ajaibID string) *CentrifugoManager {
	return &CentrifugoManager{
		ajaibID:     ajaibID,
		clients:     make(map[string]*cfClientState),
		channelSubs: make(map[string]map[string]bool),
		publishers:  make(map[string]context.CancelFunc),
	}
}

func (cm *CentrifugoManager) GetAjaibID() string {
	cm.ajaibIDMu.RLock()
	defer cm.ajaibIDMu.RUnlock()
	return cm.ajaibID
}

func (cm *CentrifugoManager) SetAjaibID(id string) {
	cm.ajaibIDMu.Lock()
	defer cm.ajaibIDMu.Unlock()
	cm.ajaibID = id
	log.Printf("[CENTRIFUGE] AjaibID updated to: %s", id)
}

// InitNode creates and configures the Centrifuge node.
func (cm *CentrifugoManager) InitNode() error {
	node, err := centrifuge.New(centrifuge.Config{})
	if err != nil {
		return err
	}

	node.OnConnecting(func(ctx context.Context, e centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
		return centrifuge.ConnectReply{
			Credentials: &centrifuge.Credentials{
				UserID: cm.GetAjaibID(),
			},
		}, nil
	})

	node.OnConnect(func(client *centrifuge.Client) {
		log.Printf("[CENTRIFUGE] Client connected: %s (user: %s)", client.ID(), client.UserID())

		connType := "public"

		cm.clientsMu.Lock()
		cm.clients[client.ID()] = &cfClientState{
			clientID:  client.ID(),
			userID:    client.UserID(),
			connectAt: time.Now(),
			channels:  make(map[string]bool),
			connType:  connType,
		}
		cm.clientsMu.Unlock()

		client.OnSubscribe(func(e centrifuge.SubscribeEvent, cb centrifuge.SubscribeCallback) {
			log.Printf("[CENTRIFUGE] Client %s subscribed to %s", client.ID(), e.Channel)

			// Track subscription
			cm.clientsMu.Lock()
			if cs, ok := cm.clients[client.ID()]; ok {
				cs.channels[e.Channel] = true
				if strings.Contains(e.Channel, "margin:") || strings.Contains(e.Channel, "position:") || strings.Contains(e.Channel, "order:") {
					cs.connType = "private"
				}
			}
			cm.clientsMu.Unlock()

			cm.channelSubsMu.Lock()
			isFirstSub := false
			if cm.channelSubs[e.Channel] == nil {
				cm.channelSubs[e.Channel] = make(map[string]bool)
				isFirstSub = true
			}
			cm.channelSubs[e.Channel][client.ID()] = true
			cm.channelSubsMu.Unlock()

			if isFirstSub {
				cm.startPublishersForChannel(e.Channel)
			}

			cb(centrifuge.SubscribeReply{}, nil)
		})

		client.OnUnsubscribe(func(e centrifuge.UnsubscribeEvent) {
			log.Printf("[CENTRIFUGE] Client %s unsubscribed from %s", client.ID(), e.Channel)
			cm.handleUnsubscribe(client.ID(), e.Channel)
		})

		client.OnDisconnect(func(e centrifuge.DisconnectEvent) {
			log.Printf("[CENTRIFUGE] Client disconnected: %s (reason: %s)", client.ID(), e.Reason)

			cm.clientsMu.Lock()
			if cs, ok := cm.clients[client.ID()]; ok {
				for ch := range cs.channels {
					cm.handleUnsubscribe(client.ID(), ch)
				}
				delete(cm.clients, client.ID())
			}
			cm.clientsMu.Unlock()
		})
	})

	if err := node.Run(); err != nil {
		return err
	}

	cm.node = node
	return nil
}

func (cm *CentrifugoManager) handleUnsubscribe(clientID string, channel string) {
	cm.clientsMu.Lock()
	if cs, ok := cm.clients[clientID]; ok {
		delete(cs.channels, channel)
	}
	cm.clientsMu.Unlock()

	cm.channelSubsMu.Lock()
	if subs, ok := cm.channelSubs[channel]; ok {
		delete(subs, clientID)
		if len(subs) == 0 {
			delete(cm.channelSubs, channel)

			// Stop publishers
			if cancel, exists := cm.publishers[channel]; exists {
				cancel()
				delete(cm.publishers, channel)
			}
		}
	}
	cm.channelSubsMu.Unlock()
}

func (cm *CentrifugoManager) startPublishersForChannel(channel string) {
	ajaibID := cm.GetAjaibID()

	var matchedConfig *ConnectionConfig
	var extractedVars map[string]string

	// Find matching config
	for _, m := range mockDB.ListConnections() {
		if m.Category != "centrifugo" || !m.Enabled {
			continue
		}

		pattern := strings.ReplaceAll(m.ChannelName, "{{ajaibId}}", ajaibID)
		isMatch, vars := ExtractVariablesFromChannel(pattern, channel)
		if isMatch {
			matchedConfig = &m
			extractedVars = vars
			break
		}
	}

	if matchedConfig == nil || len(matchedConfig.PeriodicEvents) == 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cm.publishers[channel] = cancel

	engCtx := EngineContext{
		Vars: extractedVars,
	}
	if engCtx.Vars == nil {
		engCtx.Vars = make(map[string]string)
	}

	for _, event := range matchedConfig.PeriodicEvents {
		if event.RateMs <= 0 {
			continue
		}

		go func(evt PeriodicEvent, ch string, eCtx EngineContext) {
			// Calculate initial rate
			rate := time.Duration(float64(evt.RateMs)/GetGlobalRateMultiplier()) * time.Millisecond
			timer := time.NewTimer(rate)
			defer timer.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					payload := GenerateMockPayload(matchedConfig.ID, evt.Template, evt.Randomizers, eCtx)
					_, err := cm.node.Publish(ch, payload)
					if err != nil {
						log.Printf("[CENTRIFUGE] Error publishing to %s: %v", ch, err)
					}
					// Recalculate rate for the next tick in case the multiplier changed
					nextRate := time.Duration(float64(evt.RateMs)/GetGlobalRateMultiplier()) * time.Millisecond
					timer.Reset(nextRate)
				}
			}
		}(event, channel, engCtx)
	}
}

// GetNode returns the centrifuge node for handler mounting.
func (cm *CentrifugoManager) GetNode() *centrifuge.Node {
	return cm.node
}

// GetStatus returns a snapshot of the Centrifuge state for the dashboard.
func (cm *CentrifugoManager) GetStatus() CentrifugoStatus {
	cm.clientsMu.RLock()
	clients := make([]CentrifugoClientInfo, 0, len(cm.clients))
	for _, cs := range cm.clients {
		channels := make([]string, 0, len(cs.channels))
		for ch := range cs.channels {
			channels = append(channels, ch)
		}
		clients = append(clients, CentrifugoClientInfo{
			ClientID:       cs.clientID,
			UserID:         cs.userID,
			ConnectAt:      cs.connectAt.Format(time.RFC3339),
			Duration:       time.Since(cs.connectAt).Round(time.Second).String(),
			Channels:       channels,
			ConnectionType: cs.connType,
		})
	}
	cm.clientsMu.RUnlock()

	cm.channelSubsMu.RLock()
	channels := make([]CentrifugoChannelInfo, 0, len(cm.channelSubs))
	for ch, subs := range cm.channelSubs {
		chType := "public"
		if strings.HasPrefix(ch, "margin:") || strings.HasPrefix(ch, "position:") || strings.HasPrefix(ch, "order:") {
			chType = "private"
		}
		channels = append(channels, CentrifugoChannelInfo{
			Channel:  ch,
			Type:     chType,
			SubCount: len(subs),
		})
	}
	cm.channelSubsMu.RUnlock()

	return CentrifugoStatus{
		TotalClients:  len(clients),
		TotalChannels: len(channels),
		Clients:       clients,
		Channels:      channels,
		AjaibID:       cm.GetAjaibID(),
	}
}

// DisconnectAll disconnects all centrifuge clients.
func (cm *CentrifugoManager) DisconnectAll() {
	cm.clientsMu.RLock()
	clientIDs := make([]string, 0, len(cm.clients))
	for _, cs := range cm.clients {
		clientIDs = append(clientIDs, cs.userID)
	}
	cm.clientsMu.RUnlock()

	seen := map[string]bool{}
	for _, uid := range clientIDs {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		cm.node.Disconnect(uid, centrifuge.WithCustomDisconnect(centrifuge.Disconnect{
			Code:   3000,
			Reason: "Dashboard requested disconnect",
		}))
	}
	log.Printf("[CENTRIFUGE] Disconnected all clients")
}

func (cm *CentrifugoManager) DisconnectChannel(channel string) {
	cm.channelSubsMu.RLock()
	subs, ok := cm.channelSubs[channel]
	clientIDs := make([]string, 0, len(subs))
	for clientID := range subs {
		clientIDs = append(clientIDs, clientID)
	}
	cm.channelSubsMu.RUnlock()

	if !ok {
		return
	}

	cm.clientsMu.RLock()
	userIDs := make([]string, 0)
	for _, cid := range clientIDs {
		if cs, exists := cm.clients[cid]; exists {
			userIDs = append(userIDs, cs.userID)
		}
	}
	cm.clientsMu.RUnlock()

	for _, uid := range userIDs {
		cm.node.Disconnect(uid, centrifuge.WithCustomDisconnect(centrifuge.Disconnect{
			Code:   3000,
			Reason: fmt.Sprintf("Channel %s disconnected by dashboard", channel),
		}))
	}
	log.Printf("[CENTRIFUGE] Disconnected channel %s", channel)
}

func (cm *CentrifugoManager) DisconnectType(connType string) {
	cm.clientsMu.RLock()
	userIDs := make([]string, 0)
	for _, cs := range cm.clients {
		if cs.connType == connType {
			userIDs = append(userIDs, cs.userID)
		}
	}
	cm.clientsMu.RUnlock()

	for _, uid := range userIDs {
		cm.node.Disconnect(uid, centrifuge.WithCustomDisconnect(centrifuge.Disconnect{
			Code:   3000,
			Reason: fmt.Sprintf("Dashboard disconnected %s channels", connType),
		}))
	}
	log.Printf("[CENTRIFUGE] Disconnected all %s channels", connType)
}

var cfManager *CentrifugoManager
