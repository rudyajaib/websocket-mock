package main

import (
	"context"
	"encoding/json"
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

type ClientConn struct {
	conn    *websocket.Conn
	cancel  context.CancelFunc
	writeMu sync.Mutex
}

type MockServer struct {
	clients       map[*ClientConn]bool
	clientsMu     sync.Mutex
	tickMs        int64
	orderBookSize int64
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
		c.cancel()
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

var server = &MockServer{
	clients:       make(map[*ClientConn]bool),
	tickMs:        1000,
	orderBookSize: 30,
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// --- Request Data Structures ---

type ClientMessage struct {
	AssetID            string `json:"asset_id"`
	QuoteCurrency      string `json:"quote_currency"`
	StreamAction       string `json:"stream_action"`
	StreamType         string `json:"stream_type"`
	ID                 any    `json:"id"`
	Source             string `json:"source"`
	Rate               string `json:"rate"`
	OrderBookSize      string `json:"order_book_size"` // New dynamic command
	CloseAllConnection bool   `json:"close_all_connection"`
	SimulatePeerClosed bool   `json:"simulate_peer_closed"`
}

type ConnectResponse struct {
	ConnectionID string `json:"connection_id"`
	Result       string `json:"result"`
}

type TickRateResponse struct {
	CurrentRate string `json:"current_rate"`
}

type OrderBookSizeResponse struct {
	CurrentOrderBookSize string `json:"current_order_book_size"`
}

type CloseAllResponse struct {
	CloseAllConnection bool `json:"close_all_connection"`
}

// --- V3 Price Models ---
type PriceUpdateV3 struct {
	AssetID              string `json:"asset_id"`
	QuoteCurrency        string `json:"quote_currency"`
	Timestamp            int64  `json:"timestamp"`
	PriceLabel           string `json:"price_label"`
	PriceChangeLabel     string `json:"price_change_label"`
	PriceRateChangeLabel string `json:"price_rate_change_label"`
	MarkPriceLabel       string `json:"mark_price_label"`
}

// --- V2 Price Models ---
type PriceUpdateV2 struct {
	AssetID              string  `json:"asset_id"`
	QuoteCurrency        string  `json:"quote_currency"`
	Timestamp            int64   `json:"timestamp"`
	PriceLabel           string  `json:"price_label"`
	Price                float64 `json:"price"`
	PriceInIDRLabel      string  `json:"price_in_idr_label"`
	PriceChangeLabel     string  `json:"price_change_label"`
	PriceRateChangeLabel string  `json:"price_rate_change_label"`
	StreamerReceivedAt   any     `json:"streamer_received_at"`
	LPTimestamp          int64   `json:"lp_timestamp"`
	P                    int     `json:"p"`
	Co                   int     `json:"co"`
	Pe                   int64   `json:"pe"`
}

// --- V3 Order Book Models ---
type OrderBookEntryV3 struct {
	PriceLabel    string  `json:"price_label"`
	Quantity      float64 `json:"quantity"`
	QuantityLabel string  `json:"quantity_label"`
}

type OrderBookUpdateV3 struct {
	AssetID          string             `json:"asset_id"`
	QuoteCurrency    string             `json:"quote_currency"`
	Timestamp        int64              `json:"timestamp"`
	Asks             []OrderBookEntryV3 `json:"asks"`
	Bids             []OrderBookEntryV3 `json:"bids"`
	AskQtyPercentage string             `json:"ask_qty_percentage"`
	BidQtyPercentage string             `json:"bid_qty_percentage"`
	SumQty           string             `json:"sum_qty"`
}

// --- V2 Order Book Models ---
type OrderBookEntryV2 struct {
	QuantityLabel string  `json:"quantity_label"`
	PriceLabel    string  `json:"price_label"`
	Price         float64 `json:"price"`
	Quantity      float64 `json:"quantity"`
}

type OrderBookUpdateV2 struct {
	AssetID          string             `json:"asset_id"`
	QuoteCurrency    string             `json:"quote_currency"`
	Timestamp        int64              `json:"timestamp"`
	Asks             []OrderBookEntryV2 `json:"asks"`
	Bids             []OrderBookEntryV2 `json:"bids"`
	AskQtyPercentage float64            `json:"ask_qty_percentage"`
	BidQtyPercentage float64            `json:"bid_qty_percentage"`
	SumQty           float64            `json:"sum_qty"`
	P                int                `json:"p"`
	Co               int                `json:"co"`
	Pe               int64              `json:"pe"`
}

// --- Futures Market Trade Models ---
type FuturesMarketTrade struct {
	Side         string  `json:"side"`
	Symbol       string  `json:"symbol"`
	Price        float64 `json:"price"`
	Size         float64 `json:"size"`
	Timestamp    float64 `json:"timestamp"`
	ExchangeName string  `json:"exchange_name"`
}

type FuturesMarketTradesUpdate struct {
	Trades []FuturesMarketTrade `json:"trades"`
	Symbol string               `json:"symbol"`
}

// --- Handlers ---

func handlePriceSocketV3(w http.ResponseWriter, r *http.Request) {
	serveSocket(w, r, "PRICE_V3")
}

func handlePriceSocketV2(w http.ResponseWriter, r *http.Request) {
	serveSocket(w, r, "PRICE_V2")
}

func handleOrderBookSocketV3(w http.ResponseWriter, r *http.Request) {
	serveSocket(w, r, "ORDER_BOOK_V3")
}

func handleOrderBookSocketV2(w http.ResponseWriter, r *http.Request) {
	serveSocket(w, r, "ORDER_BOOK_V2")
}

func handleFuturesMarketTradeSocket(w http.ResponseWriter, r *http.Request) {
	serveSocket(w, r, "FUTURES_MARKET_TRADE")
}

func serveSocket(w http.ResponseWriter, r *http.Request, endpointType string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
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

		basePriceV3 := 1175644449.0
		basePriceV2 := 698.2924

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

				currentOBSize := int(atomic.LoadInt64(&server.orderBookSize))

				for _, subStr := range subs {
					parts := strings.Split(subStr, "/")
					if len(parts) != 2 {
						continue
					}
					asset, quote := parts[0], parts[1]
					ts := time.Now().UnixNano() / int64(time.Millisecond)

					if endpointType == "PRICE_V3" {
						basePriceV3 += (rand.Float64() * 1000) - 500
						update := PriceUpdateV3{
							AssetID:              asset,
							QuoteCurrency:        quote,
							Timestamp:            ts,
							PriceLabel:           fmt.Sprintf("%.0f", basePriceV3),
							PriceChangeLabel:     fmt.Sprintf("%.2f", (rand.Float64()*100)-50),
							PriceRateChangeLabel: fmt.Sprintf("%.2f", (rand.Float64()*4)-2),
							MarkPriceLabel:       fmt.Sprintf("%.0f", basePriceV3+((rand.Float64()*200)-100)),
						}
						safeWriteJSON(update)

					} else if endpointType == "PRICE_V2" {
						basePriceV2 += (rand.Float64() * 10) - 5
						priceChange := (rand.Float64() * 100) - 50
						priceRateChange := (rand.Float64() * 20) - 10

						update := PriceUpdateV2{
							AssetID:              asset,
							QuoteCurrency:        quote,
							Timestamp:            ts,
							PriceLabel:           formatDecimalComma(basePriceV2, 4),
							Price:                basePriceV2,
							PriceInIDRLabel:      formatDecimalComma(basePriceV2, 4),
							PriceChangeLabel:     formatDecimalComma(priceChange, 6),
							PriceRateChangeLabel: formatDecimalComma(priceRateChange, 2),
							StreamerReceivedAt:   nil,
							LPTimestamp:          time.Now().UnixNano() / int64(time.Microsecond),
							P:                    0,
							Co:                   0,
							Pe:                   ts,
						}
						safeWriteJSON(update)

					} else if endpointType == "ORDER_BOOK_V3" {
						safeWriteJSON(generateOrderBookV3(asset, quote, basePriceV3, currentOBSize))

					} else if endpointType == "ORDER_BOOK_V2" {
						safeWriteJSON(generateOrderBookV2(asset, quote, basePriceV3, currentOBSize))

					} else if endpointType == "FUTURES_MARKET_TRADE" {
						safeWriteJSON(generateFuturesMarketTrades(asset, quote, basePriceV3))
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

				if msg.SimulatePeerClosed {
					go server.BroadcastPeerClosed(client)
					continue
				}

				if msg.CloseAllConnection {
					safeWriteJSON(CloseAllResponse{CloseAllConnection: true})
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

				if msg.OrderBookSize != "" {
					parsedSize, err := strconv.ParseInt(msg.OrderBookSize, 10, 64)
					if err == nil && parsedSize > 0 {
						atomic.StoreInt64(&server.orderBookSize, parsedSize)
						safeWriteJSON(OrderBookSizeResponse{CurrentOrderBookSize: msg.OrderBookSize})
						log.Printf("Global order book size adjusted to %d", parsedSize)
					}
					continue
				}

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

					resStr := fmt.Sprintf("%s successfully unsubscribe %s with asset id %s quote currency %s",
						connID, endpointType, msg.AssetID, msg.QuoteCurrency)

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

// --- Formatters ---

func formatIDRPrice(price float64) string {
	s := fmt.Sprintf("%.0f", price)
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, '.')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

func formatDecimalComma(val float64, precision int) string {
	s := strconv.FormatFloat(val, 'f', precision, 64)
	return strings.Replace(s, ".", ",", 1)
}

// --- Generators ---

func generateFuturesMarketTrades(asset, quote string, fallbackBasePrice float64) FuturesMarketTradesUpdate {
	basePrice := fallbackBasePrice
	if strings.ToUpper(quote) == "USDT" {
		basePrice = 69000.0
	}

	numTrades := rand.Intn(3) + 1
	trades := make([]FuturesMarketTrade, 0, numTrades)

	for i := 0; i < numTrades; i++ {
		priceOffset := (rand.Float64() * 100) - 50
		price := basePrice + priceOffset

		side := "BUY"
		if rand.Float32() > 0.5 {
			side = "SELL"
		}

		size := rand.Float64() * 0.05

		trades = append(trades, FuturesMarketTrade{
			Side:         side,
			Symbol:       asset + "_" + quote,
			Price:        price,
			Size:         size,
			Timestamp:    float64(time.Now().UnixNano() / int64(time.Millisecond)),
			ExchangeName: "Binance",
		})
	}

	return FuturesMarketTradesUpdate{
		Trades: trades,
		Symbol: asset + "_" + quote,
	}
}

func generateOrderBookV2(asset, quote string, basePrice float64, size int) OrderBookUpdateV2 {
	asks := make([]OrderBookEntryV2, 0, size)
	bids := make([]OrderBookEntryV2, 0, size)
	ts := time.Now().UnixNano() / int64(time.Millisecond)

	for i := 0; i < size; i++ {
		price := basePrice + float64((i+1)*1500) + (rand.Float64() * 500)
		qty := rand.Float64() * 0.7
		asks = append(asks, OrderBookEntryV2{
			QuantityLabel: formatDecimalComma(qty, 6),
			PriceLabel:    formatIDRPrice(price),
			Price:         price,
			Quantity:      qty,
		})
	}
	sort.Slice(asks, func(i, j int) bool { return asks[i].Price < asks[j].Price })

	for i := 0; i < size; i++ {
		price := basePrice - float64((i+1)*1500) - (rand.Float64() * 500)
		qty := rand.Float64() * 0.3
		bids = append(bids, OrderBookEntryV2{
			QuantityLabel: formatDecimalComma(qty, 6),
			PriceLabel:    formatIDRPrice(price),
			Price:         price,
			Quantity:      qty,
		})
	}
	sort.Slice(bids, func(i, j int) bool { return bids[i].Price > bids[j].Price })

	return OrderBookUpdateV2{
		AssetID:          asset,
		QuoteCurrency:    quote,
		Timestamp:        ts,
		Asks:             asks,
		Bids:             bids,
		AskQtyPercentage: 58.0 + (rand.Float64() * 5.0),
		BidQtyPercentage: 42.0 - (rand.Float64() * 5.0),
		SumQty:           1.5 + rand.Float64(),
		P:                119 + rand.Intn(10),
		Co:               124 + rand.Intn(10),
		Pe:               ts + 124,
	}
}

func generateOrderBookV3(asset, quote string, basePrice float64, size int) OrderBookUpdateV3 {
	asks := make([]OrderBookEntryV3, 0, size)
	bids := make([]OrderBookEntryV3, 0, size)

	for i := 0; i < size; i++ {
		price := basePrice + float64((i+1)*5000000) + (rand.Float64() * 1000000)
		qty := rand.Float64() * 0.1
		asks = append(asks, OrderBookEntryV3{
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

	for i := 0; i < size; i++ {
		price := basePrice - float64((i+1)*20000000) - (rand.Float64() * 1000000)
		if price < 1000000 {
			price = 1000000
		}
		qty := rand.Float64() * 1.5
		bids = append(bids, OrderBookEntryV3{
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

	return OrderBookUpdateV3{
		AssetID:          asset,
		QuoteCurrency:    quote,
		Timestamp:        time.Now().UnixNano() / int64(time.Millisecond),
		Asks:             asks,
		Bids:             bids,
		AskQtyPercentage: fmt.Sprintf("%.0f", 58.0+(rand.Float64()*5.0)),
		BidQtyPercentage: fmt.Sprintf("%.0f", 42.0-(rand.Float64()*5.0)),
		SumQty:           fmt.Sprintf("%.0f", 200000+(rand.Float64()*10000)),
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())

	http.HandleFunc("/ws/v3/coin-data/price", handlePriceSocketV3)
	http.HandleFunc("/ws/v2/coin-data/price", handlePriceSocketV2)

	http.HandleFunc("/ws/v3/coin-data/order-book", handleOrderBookSocketV3)
	http.HandleFunc("/ws/v2/coin-data/order-book", handleOrderBookSocketV2)

	http.HandleFunc("/ws/coin-data/futures/market-trade", handleFuturesMarketTradeSocket)

	port := ":8080"
	log.Printf("Starting WebSocket mock server on ws://localhost%s", port)
	log.Printf("- Price Socket (V3): ws://localhost%s/ws/v3/coin-data/price", port)
	log.Printf("- Price Socket (V2): ws://localhost%s/ws/v2/coin-data/price", port)
	log.Printf("- Order Book (V3):   ws://localhost%s/ws/v3/coin-data/order-book", port)
	log.Printf("- Order Book (V2):   ws://localhost%s/ws/v2/coin-data/order-book", port)
	log.Printf("- Futures Trade:     ws://localhost%s/ws/coin-data/futures/market-trade", port)

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}
