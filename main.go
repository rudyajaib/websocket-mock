package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// --- Global Server State ---

// ClientConn encapsulates a single connection and its thread-safe write lock
type ClientConn struct {
	conn    *websocket.Conn
	cancel  context.CancelFunc
	writeMu sync.Mutex
}

type MockServer struct {
	clients   map[*ClientConn]bool
	clientsMu sync.Mutex
	tickMs    int64 // Global tick rate in milliseconds
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

// CloseAll abruptly closes all OTHER connections
func (s *MockServer) CloseAll(sender *ClientConn) {
	s.clientsMu.Lock()
	clientsCopy := make([]*ClientConn, 0, len(s.clients))
	for c := range s.clients {
		if c != sender { // Skip the sender
			clientsCopy = append(clientsCopy, c)
		}
	}
	s.clientsMu.Unlock()

	log.Printf("Abruptly closing %d OTHER active connection(s)...", len(clientsCopy))
	for _, c := range clientsCopy {
		c.cancel()     // Signal goroutines to stop
		c.conn.Close() // Drop the TCP socket immediately
	}
}

// BroadcastPeerClosed sends a normal close frame gracefully to ALL OTHER clients
func (s *MockServer) BroadcastPeerClosed(sender *ClientConn) {
	s.clientsMu.Lock()
	clientsCopy := make([]*ClientConn, 0, len(s.clients))
	for c := range s.clients {
		if c != sender { // Skip the sender
			clientsCopy = append(clientsCopy, c)
		}
	}
	s.clientsMu.Unlock()

	log.Printf("Broadcasting graceful peer closed to %d OTHER client(s)...", len(clientsCopy))
	closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Server requested graceful close")

	for _, c := range clientsCopy {
		// Run each disconnect in a goroutine so we don't block
		go func(client *ClientConn) {
			client.writeMu.Lock()
			client.conn.WriteMessage(websocket.CloseMessage, closeMsg)
			client.writeMu.Unlock()

			// Give Starscream on iOS 100ms to process the frame before dropping TCP
			time.Sleep(100 * time.Millisecond)
			client.cancel()
			client.conn.Close()
		}(c)
	}
}

var server = &MockServer{
	clients: make(map[*ClientConn]bool),
	tickMs:  1000, // Default 1 second
}

// --- Data Structures ---

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

type ClientMessage struct {
	AssetID            string `json:"asset_id"`
	QuoteCurrency      string `json:"quote_currency"`
	StreamAction       string `json:"stream_action"`
	StreamType         string `json:"stream_type"`
	ID                 any    `json:"id"`
	Source             string `json:"source"`
	Rate               string `json:"rate"`                 // Global tick rate adjust
	CloseAllConnection bool   `json:"close_all_connection"` // Global abrupt close
	SimulatePeerClosed bool   `json:"simulate_peer_closed"` // Global graceful close broadcast
}

type ConnectResponse struct {
	ConnectionID string `json:"connection_id"`
	Result       string `json:"result"`
}

type TickRateResponse struct {
	CurrentRate string `json:"current_rate"`
}

type CloseAllResponse struct {
	CloseAllConnection bool `json:"close_all_connection"`
}

// Price Models
type PriceUpdate struct {
	AssetID              string `json:"asset_id"`
	QuoteCurrency        string `json:"quote_currency"`
	Timestamp            int64  `json:"timestamp"`
	PriceLabel           string `json:"price_label"`
	PriceChangeLabel     string `json:"price_change_label"`
	PriceRateChangeLabel string `json:"price_rate_change_label"`
	MarkPriceLabel       string `json:"mark_price_label"`
}

// Order Book Models
type OrderBookEntry struct {
	PriceLabel    string  `json:"price_label"`
	Quantity      float64 `json:"quantity"`
	QuantityLabel string  `json:"quantity_label"`
}

type OrderBookUpdate struct {
	AssetID          string           `json:"asset_id"`
	QuoteCurrency    string           `json:"quote_currency"`
	Timestamp        int64            `json:"timestamp"`
	Asks             []OrderBookEntry `json:"asks"`
	Bids             []OrderBookEntry `json:"bids"`
	AskQtyPercentage string           `json:"ask_qty_percentage"`
	BidQtyPercentage string           `json:"bid_qty_percentage"`
	SumQty           string           `json:"sum_qty"`
}

// --- Handlers ---

func handlePriceSocket(w http.ResponseWriter, r *http.Request) {
	serveSocket(w, r, "PRICE")
}

func handleOrderBookSocket(w http.ResponseWriter, r *http.Request) {
	serveSocket(w, r, "ORDER_BOOK")
}

// serveSocket is the shared connection logic for both endpoints
func serveSocket(w http.ResponseWriter, r *http.Request, endpointType string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Create our thread-safe client connection state
	client := &ClientConn{
		conn:   conn,
		cancel: cancel,
	}
	server.AddClient(client)

	defer func() {
		cancel()
		server.RemoveClient(client)
		conn.Close()
	}()

	connID := fmt.Sprintf("%d-130016023-100.72.69.13:55992", rand.Intn(900000)+100000)
	log.Printf("Client connected to %s. Connection ID: %s", endpointType, connID)

	safeWriteJSON := func(v interface{}) error {
		client.writeMu.Lock()
		defer client.writeMu.Unlock()
		return client.conn.WriteJSON(v)
	}

	var subMu sync.RWMutex
	activeSubs := make(map[string]bool)

	// Write Pump
	go func() {
		rate := atomic.LoadInt64(&server.tickMs)
		timer := time.NewTimer(time.Duration(rate) * time.Millisecond)
		defer timer.Stop()

		basePrice := 1000000000.0 // 1 Billion Base

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				subMu.RLock()
				subs := make([]string, 0, len(activeSubs))
				for sub := range activeSubs {
					subs = append(subs, sub)
				}
				subMu.RUnlock()

				for _, subStr := range subs {
					parts := strings.Split(subStr, "/")
					if len(parts) != 2 {
						continue
					}
					asset, quote := parts[0], parts[1]

					if endpointType == "PRICE" {
						basePrice += (rand.Float64() * 1000) - 500
						update := PriceUpdate{
							AssetID:              asset,
							QuoteCurrency:        quote,
							Timestamp:            time.Now().UnixNano() / int64(time.Millisecond),
							PriceLabel:           fmt.Sprintf("%.0f", basePrice),
							PriceChangeLabel:     fmt.Sprintf("%.2f", (rand.Float64()*100)-50),
							PriceRateChangeLabel: fmt.Sprintf("%.2f", (rand.Float64()*4)-2),
							MarkPriceLabel:       fmt.Sprintf("%.0f", basePrice+((rand.Float64()*200)-100)),
						}
						safeWriteJSON(update)

					} else if endpointType == "ORDER_BOOK" {
						safeWriteJSON(generateOrderBook(asset, quote, basePrice))
					}
				}

				newRate := atomic.LoadInt64(&server.tickMs)
				if newRate <= 0 {
					newRate = 100
				}
				timer.Reset(time.Duration(newRate) * time.Millisecond)
			}
		}
	}()

	// Read Pump
	for {
		messageType, p, err := client.conn.ReadMessage()
		if err != nil {
			log.Println("Read error or client disconnected:", err)
			break
		}

		if messageType == websocket.TextMessage {
			msgStr := strings.TrimSpace(string(p))

			if msgStr == "Ping" {
				client.writeMu.Lock()
				client.conn.WriteMessage(websocket.TextMessage, []byte("Pong"))
				client.writeMu.Unlock()
				continue
			}

			var msg ClientMessage
			if err := json.Unmarshal(p, &msg); err == nil {

				// --- 1. Global Actions ---
				if msg.SimulatePeerClosed {
					// Pass the current client pointer to filter it out
					go server.BroadcastPeerClosed(client)
					continue
				}

				if msg.CloseAllConnection {
					safeWriteJSON(CloseAllResponse{CloseAllConnection: true})
					// Pass the current client pointer to filter it out
					go server.CloseAll(client)
					continue
				}

				if msg.Rate != "" {
					parsedRate, err := strconv.ParseInt(msg.Rate, 10, 64)
					if err == nil && parsedRate > 0 {
						atomic.StoreInt64(&server.tickMs, parsedRate)
						safeWriteJSON(TickRateResponse{CurrentRate: msg.Rate})
						log.Printf("Global tick rate adjusted to %d ms", parsedRate)
					}
					continue
				}

				// --- 2. Subscription Actions ---
				subKey := fmt.Sprintf("%s/%s", msg.AssetID, msg.QuoteCurrency)

				switch msg.StreamAction {
				case "SUBSCRIBE":
					subMu.Lock()
					activeSubs[subKey] = true
					subMu.Unlock()

					safeWriteJSON(ConnectResponse{
						ConnectionID: connID,
						Result:       "successfully subscribe",
					})
					log.Printf("Client subscribed to %s on %s", subKey, endpointType)

				case "UNSUBSCRIBE":
					subMu.Lock()
					delete(activeSubs, subKey)
					subMu.Unlock()

					resStr := fmt.Sprintf("%s successfully unsubscribe %s_V3 with asset id %s quote currency %s",
						connID, msg.StreamType, msg.AssetID, msg.QuoteCurrency)

					safeWriteJSON(ConnectResponse{
						ConnectionID: connID,
						Result:       resStr,
					})
					log.Printf("Client unsubscribed from %s on %s", subKey, endpointType)
				}
			}
		}
	}
}

// --- Generators (Unchanged) ---

func generateOrderBook(asset, quote string, basePrice float64) OrderBookUpdate {
	asks := make([]OrderBookEntry, 0)
	bids := make([]OrderBookEntry, 0)

	for i := 0; i < 6; i++ {
		price := basePrice + float64((i+1)*5000000) + (rand.Float64() * 1000000)
		qty := rand.Float64() * 0.1
		asks = append(asks, OrderBookEntry{
			PriceLabel:    fmt.Sprintf("%.0f", price),
			Quantity:      qty,
			QuantityLabel: fmt.Sprintf("%.3f", qty),
		})
	}
	sort.Slice(asks, func(i, j int) bool {
		pI, _ := strconv.ParseFloat(asks[i].PriceLabel, 64)
		pJ, _ := strconv.ParseFloat(asks[j].PriceLabel, 64)
		return pI < pJ
	})

	for i := 0; i < 12; i++ {
		price := basePrice - float64((i+1)*20000000) - (rand.Float64() * 1000000)
		if price < 1000000 {
			price = 1000000
		}
		qty := rand.Float64() * 1.5
		bids = append(bids, OrderBookEntry{
			PriceLabel:    fmt.Sprintf("%.0f", price),
			Quantity:      qty,
			QuantityLabel: fmt.Sprintf("%.3f", qty),
		})
	}
	sort.Slice(bids, func(i, j int) bool {
		pI, _ := strconv.ParseFloat(bids[i].PriceLabel, 64)
		pJ, _ := strconv.ParseFloat(bids[j].PriceLabel, 64)
		return pI > pJ
	})

	return OrderBookUpdate{
		AssetID:          asset,
		QuoteCurrency:    quote,
		Timestamp:        time.Now().UnixNano() / int64(time.Millisecond),
		Asks:             asks,
		Bids:             bids,
		AskQtyPercentage: "1",
		BidQtyPercentage: "99",
		SumQty:           fmt.Sprintf("%.0f", 200000+(rand.Float64()*10000)),
	}
}

func main() {
	hostFlag := flag.String("host", "localhost", "Host address to bind to")
	portFlag := flag.String("port", "8080", "Port to bind to")

	flag.Parse()

	addr := fmt.Sprintf("%s:%s", *hostFlag, *portFlag)

	rand.Seed(time.Now().UnixNano())

	http.HandleFunc("/ws/v3/coin-data/price", handlePriceSocket)
	http.HandleFunc("/ws/v3/coin-data/order-book", handleOrderBookSocket)

	log.Printf("Starting WebSocket mock server on %s", addr)
	log.Printf("- Price Socket: ws://%s/ws/v3/coin-data/price", addr)
	log.Printf("- Order Book Socket: ws://%s/ws/v3/coin-data/order-book", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}
