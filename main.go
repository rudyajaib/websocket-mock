package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/centrifugal/centrifuge"
)

func main() {
	rand.Seed(time.Now().UnixNano())
	port := ":8080"

	// ------------------------------------------
	// A. Initialize Mock DB
	// ------------------------------------------
	mockDB = NewMockDB("mocks.json")
	if err := mockDB.Load(); err != nil {
		log.Fatalf("Failed to load mock database: %v", err)
	}

	// ------------------------------------------
	// B. Initialize Gorilla Mock Server
	// ------------------------------------------
	server = NewMockServer()

	// ------------------------------------------
	// C. Initialize Centrifuge Node
	// ------------------------------------------
	cfManager = NewCentrifugoManager("130016023")
	if err := cfManager.InitNode(); err != nil {
		log.Fatalf("Failed to initialize Centrifuge node: %v", err)
	}

	// Start Centrifuge background publishers for all defined channels
	// Publishers are now managed per-channel dynamically

	// Setup static file serving for Dashboard UIlers
	centrifugeWS := centrifuge.NewWebsocketHandler(cfManager.GetNode(), centrifuge.WebsocketConfig{})
	http.Handle("/connection/websocket", centrifugeWS)
	http.Handle("/private/connection/websocket", centrifugeWS)

	// ------------------------------------------
	// D. Gorilla WebSocket Endpoints
	// ------------------------------------------
	// Catch-all for Gorilla websocket routes
	http.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		serveSocket(w, r, r.URL.Path)
	})

	// ------------------------------------------
	// E. Dashboard & API
	// ------------------------------------------
	registerAPIRoutes()
	http.HandleFunc("/dashboard", serveDashboard)
	http.HandleFunc("/dashboard/", serveDashboard)

	// ------------------------------------------
	// F. Start Server
	// ------------------------------------------
	printStartupSummary(port)

	// Auto-open dashboard in browser
	go func() {
		time.Sleep(500 * time.Millisecond)
		openBrowser(fmt.Sprintf("http://localhost%s/dashboard", port))
	}()

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		log.Printf("Cannot auto-open browser on %s. Please visit: %s", runtime.GOOS, url)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open browser: %v. Please visit: %s", err, url)
	}
}

func printStartupSummary(port string) {
	fmt.Println("\n  🚀 WebSocket Mock Server is running!\n")
	fmt.Printf("  ➜ Dashboard:   http://localhost%s/dashboard\n", port)
	fmt.Printf("  ➜ API Status:  http://localhost%s/api/status\n\n", port)

	fmt.Println("  Centrifugo Endpoints:")
	fmt.Printf("    ├─ Public:   ws://localhost%s/connection/websocket\n", port)
	fmt.Printf("    └─ Private:  ws://localhost%s/private/connection/websocket\n\n", port)

	fmt.Println("  Gorilla Endpoints:")
	fmt.Printf("    ├─ Price V3:  ws://localhost%s/ws/v3/coin-data/price\n", port)
	fmt.Printf("    ├─ Price V2:  ws://localhost%s/ws/v2/coin-data/price\n", port)
	fmt.Printf("    ├─ OrderBook: ws://localhost%s/ws/v3/coin-data/order-book\n", port)
	fmt.Printf("    ├─ Trades:    ws://localhost%s/ws/coin-data/futures/market-trade\n", port)
	fmt.Printf("    └─ Watchlist: ws://localhost%s/ws/v2/watchlist\n\n", port)
}
