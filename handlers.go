package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	mrand "math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// ==========================================
// DATA MODELS (GORILLA PAYLOADS)
// ==========================================

// ClientMessage is used to parse incoming messages to check for built-in commands
type ClientMessage struct {
	CloseAllConnection bool `json:"close_all_connection"`
	SimulatePeerClosed bool `json:"simulate_peer_closed"`
}

type ConnectResponse struct {
	ConnectionID string `json:"connection_id"`
	Result       string `json:"result"`
}

type CloseAllResponse struct {
	CloseAllConnection bool `json:"close_all_connection"`
}

// ==========================================
// GORILLA CONNECTION HANDLER
// ==========================================

func serveSocket(w http.ResponseWriter, r *http.Request, endpointType string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	b := make([]byte, 16)
	rand.Read(b)
	connID := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])

	// =========================================================
	// Look up config ID immediately upon connection
	// =========================================================
	initialConfigID := ""
	for _, m := range mockDB.ListConnections() {
		if m.Category == "gorilla" && m.Path == endpointType {
			initialConfigID = m.ID
			break
		}
	}

	client := &ClientConn{
		conn:       conn,
		cancel:     cancel,
		ConnID:     connID,
		ConfigID:   initialConfigID, // Set ID right away!
		Endpoint:   endpointType,
		ConnectAt:  time.Now(),
		ActiveSubs: make(map[string]SubContext),
	}
	server.AddClient(client)

	defer func() {
		cancel()

		client.SubsMu.Lock()
		client.ActiveSubs = make(map[string]SubContext)
		client.SubsMu.Unlock()

		server.RemoveClient(client)
		conn.Close()
	}()

	log.Printf("[GORILLA] Client connected to %s. Connection ID: %s", endpointType, connID)

	safeWriteJSON := func(v interface{}) error {
		client.writeMu.Lock()
		defer client.writeMu.Unlock()
		return client.conn.WriteJSON(v)
	}

	safeWriteRawJSON := func(data []byte) error {
		client.writeMu.Lock()
		defer client.writeMu.Unlock()
		return client.conn.WriteMessage(websocket.TextMessage, data)
	}

	// Launch single client timer loop
	go func() {
		// 1. Change to a high-resolution base tick
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		// 2. Track when each event was last fired
		lastFired := make(map[string]time.Time)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				client.SubsMu.RLock()
				type job struct {
					cfgID  string
					evt    PeriodicEvent
					engCtx EngineContext
				}
				var jobs []job
				for _, sub := range client.ActiveSubs {
					for _, evt := range sub.Events {
						if evt.RateMs > 0 {
							jobs = append(jobs, job{sub.ConfigID, evt, sub.EngineCtx})
						}
					}
				}
				client.SubsMu.RUnlock()

				// Group jobs by Event ID to merge array responses
				groupedJobs := make(map[string][]job)
				for _, j := range jobs {
					groupedJobs[j.evt.ID] = append(groupedJobs[j.evt.ID], j)
				}

				for evID, evJobs := range groupedJobs {
					if len(evJobs) == 0 {
						continue
					}

					evt := evJobs[0].evt
					rate := time.Duration(float64(evt.RateMs)/GetGlobalRateMultiplier()) * time.Millisecond

					// 3. Check if enough time has passed based on this specific event's RateMs
					if time.Since(lastFired[evID]) < rate {
						continue
					}
					lastFired[evID] = time.Now()

					// Check if this event's template is an array
					var isArrayResponse bool
					var tplParsed interface{}
					if err := json.Unmarshal(evt.Template, &tplParsed); err == nil {
						_, isArrayResponse = tplParsed.([]interface{})
					}

					if isArrayResponse {
						// Merge all individual subscriptions into one array
						var mergedArray []interface{}
						for _, j := range evJobs {
							payload := GenerateMockPayload(j.cfgID, j.evt.Template, j.evt.Randomizers, j.engCtx)
							var parsed []interface{}
							if json.Unmarshal(payload, &parsed) == nil {
								mergedArray = append(mergedArray, parsed...)
							}
						}

						if len(mergedArray) > 0 {
							// Shuffle the merged array using the mrand alias
							mrand.Shuffle(len(mergedArray), func(i, k int) {
								mergedArray[i], mergedArray[k] = mergedArray[k], mergedArray[i]
							})

							// Randomly pick a count between 1 and the total active subs
							count := mrand.Intn(len(mergedArray)) + 1

							finalPayload, _ := json.Marshal(mergedArray[:count])
							safeWriteRawJSON(finalPayload)
						}
					} else {
						// Not an array (e.g. Price V3), send standard individual payloads
						for _, j := range evJobs {
							payload := GenerateMockPayload(j.cfgID, j.evt.Template, j.evt.Randomizers, j.engCtx)
							safeWriteRawJSON(payload)
						}
					}
				}
			}
		}
	}()

	// Read Pump
	for {
		messageType, p, err := client.conn.ReadMessage()
		if err != nil {
			log.Println("[GORILLA] Read error or client disconnected:", err)
			break
		}

		if messageType == websocket.TextMessage {
			// Preprocessing for arrays (like watchlist bulk subscribe)
			var payloads [][]byte
			var rawMap map[string]interface{}
			if err := json.Unmarshal(p, &rawMap); err == nil {
				if items, ok := rawMap["watchlist_items"].([]interface{}); ok && len(items) > 1 {
					for _, item := range items {
						singleMap := make(map[string]interface{})
						for k, v := range rawMap {
							if k == "watchlist_items" {
								singleMap[k] = []interface{}{item}
							} else {
								singleMap[k] = v
							}
						}
						singleB, _ := json.Marshal(singleMap)
						payloads = append(payloads, singleB)
					}
				} else {
					payloads = append(payloads, p)
				}
			} else {
				payloads = append(payloads, p)
			}

			for _, reqPayload := range payloads {
				msgStr := strings.TrimSpace(string(reqPayload))

				// Find matching config for this endpoint
				var matchedConfig *ConnectionConfig
				for _, m := range mockDB.ListConnections() {
					if m.Category == "gorilla" && m.Path == endpointType {
						matchedConfig = &m
						break
					}
				}

				if matchedConfig == nil || !matchedConfig.Enabled {
					log.Printf("[Gorilla] Message received on %s but no active config found.", endpointType)
					var builtIn ClientMessage
					if err := json.Unmarshal(reqPayload, &builtIn); err == nil {
						handleBuiltInCommands(builtIn, client, safeWriteJSON)
					}
					continue
				}

				client.ConfigID = matchedConfig.ID

				// Handle Ping matching
				if matchedConfig.PingRequest != nil {
					if !matchedConfig.PingRequest.IsJSON {
						if msgStr == matchedConfig.PingRequest.PingMessage {
							client.writeMu.Lock()
							client.conn.WriteMessage(websocket.TextMessage, []byte(matchedConfig.PingRequest.PongMessage))
							client.writeMu.Unlock()
							continue
						}
					} else {
						var pingJSON map[string]interface{}
						if err := json.Unmarshal(reqPayload, &pingJSON); err == nil {
							pingStr := matchedConfig.PingRequest.PingMessage
							var pingTpl map[string]interface{}
							if err := json.Unmarshal([]byte(pingStr), &pingTpl); err == nil {
								isPingMatch, _ := ExtractVariablesFromRequest([]byte(pingStr), reqPayload)
								if isPingMatch {
									safeWriteRawJSON([]byte(matchedConfig.PingRequest.PongMessage))
									continue
								}
							}
						}
					}
				}

				// Try to match against Subscribe requests
				matchedSubscribe := false
				for _, reqResp := range matchedConfig.SubscribeRequests {
					isMatch, extractedVars := ExtractVariablesFromRequest(reqResp.RequestTemplate, reqPayload)
					if isMatch {
						matchedSubscribe = true

						subKey := matchedConfig.ID
						if reqResp.SubKey != "" {
							renderedKey := reqResp.SubKey
							for k, v := range extractedVars {
								renderedKey = strings.ReplaceAll(renderedKey, "{{."+k+"}}", v)
							}
							subKey += ":" + renderedKey
						}

						engCtx := EngineContext{
							Vars: extractedVars,
						}
						engCtx.Vars["connection_id"] = connID

						if reqResp.ResponseTemplate != nil && len(reqResp.ResponseTemplate) > 2 {
							respPayload := GenerateMockPayload(matchedConfig.ID, reqResp.ResponseTemplate, nil, engCtx)
							safeWriteRawJSON(respPayload)
						}

						client.SubsMu.Lock()
						client.ActiveSubs[subKey] = SubContext{
							ConfigID:  matchedConfig.ID,
							EngineCtx: engCtx,
							Events:    matchedConfig.PeriodicEvents,
						}
						client.SubsMu.Unlock()

						log.Printf("[GORILLA] [ConnID: %s] Client SUBSCRIBED to %s", connID, subKey)
						break
					}
				}
				if matchedSubscribe {
					continue
				}

				// Try to match against Unsubscribe requests
				matchedUnsubscribe := false
				for _, reqResp := range matchedConfig.UnsubscribeRequests {
					isMatch, extractedVars := ExtractVariablesFromRequest(reqResp.RequestTemplate, reqPayload)
					if isMatch {
						matchedUnsubscribe = true

						subKey := matchedConfig.ID
						if reqResp.SubKey != "" {
							renderedKey := reqResp.SubKey
							for k, v := range extractedVars {
								renderedKey = strings.ReplaceAll(renderedKey, "{{."+k+"}}", v)
							}
							subKey += ":" + renderedKey
						}

						client.SubsMu.Lock()
						delete(client.ActiveSubs, subKey)
						client.SubsMu.Unlock()

						engCtx := EngineContext{
							Vars: extractedVars,
						}
						engCtx.Vars["connection_id"] = connID

						if reqResp.ResponseTemplate != nil && len(reqResp.ResponseTemplate) > 2 {
							respPayload := GenerateMockPayload(matchedConfig.ID, reqResp.ResponseTemplate, nil, engCtx)
							safeWriteRawJSON(respPayload)
						}

						log.Printf("[GORILLA] [ConnID: %s] Client UNSUBSCRIBED from %s", connID, subKey)
						break
					}
				}
				if matchedUnsubscribe {
					continue
				}

				// If no config matched, check for built-in commands
				var builtIn ClientMessage
				if err := json.Unmarshal(reqPayload, &builtIn); err == nil {
					handleBuiltInCommands(builtIn, client, safeWriteJSON)
				}
			}
		}
	}
}

func handleBuiltInCommands(msg ClientMessage, client *ClientConn, safeWriteJSON func(interface{}) error) {
	if msg.SimulatePeerClosed {
		go server.BroadcastPeerClosed(client)
		return
	}

	if msg.CloseAllConnection {
		safeWriteJSON(CloseAllResponse{CloseAllConnection: true})
		go server.CloseAll(client)
		return
	}
}
