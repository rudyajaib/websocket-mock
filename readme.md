# Websocket
➜ Dashboard:   http://localhost:8080/dashboard
➜ API Status:  http://localhost:8080/api/status

Centrifugo Endpoints:
  ├─ Public:   ws://localhost:8080/connection/websocket
  └─ Private:  ws://localhost:8080/private/connection/websocket

Gorilla Endpoints:
  ├─ Price V3:  ws://localhost:8080/ws/v3/coin-data/price
  ├─ Price V2:  ws://localhost:8080/ws/v2/coin-data/price
  ├─ OrderBook: ws://localhost:8080/ws/v3/coin-data/order-book
  ├─ Trades:    ws://localhost:8080/ws/coin-data/futures/market-trade
  └─ Watchlist: ws://localhost:8080/ws/v2/watchlist

# Debug with wscat
To debug, you can open connection with `wscat`, install it via npm or homebrew.

## Gorilla (old websocket)
### Open connection:
```
wscat -c ws://localhost:8080/ws/v3/coin-data/price
```

### Ping:
```
Ping
```

### Subscribe:
With wscat, make sure to send json in one liner.
```
{"stream_type":"PRICE","stream_action":"SUBSCRIBE","source":"COIN_DETAIL","asset_id":"PERP-ETH","id":1,"quote_currency":"IDR"}
```

## Centrifugo
In centrifugo, they will use build in ping pong. If client did not response any ping pong, then server will close it immediately. Thus, to connect with wscat, we need to send additional parameter with it:
### Open Connection
```
wscat -c "ws://localhost:8080/private/connection/websocket?cf_ws_frame_ping_pong=true"
```

### Connect:
```
{"id": 1, "connect": {}}
```

### Connect with token:
```
{"id": 1, "connect": {"token": "eyJ0eXAiOiJBQ0NFU1MiLCJhbGciOiJFUzI1NiJ9.eyJhamFpYiI6IjEzMDAxNjAyMy5DX0lPUy5BQ0NFU1MiLCJwbGF0Zm9ybSI6IkNfSU9TIiwic2VjdXJpdHlfaWQiOiIiLCJkZXZpY2Vfc2lnbmF0dXJlIjoiM0Q3RDdFMTUtMjY0Ny00RjlFLTk4RkUtNzdDMEE1NDU3QjAzYXJtNjRpT1MiLCJhdWQiOiJodHRwczovL2FwcC5hamFpYi5jby5pZCIsImV4cCI6MTc4NDUzMDM4OCwianRpIjoiZjI4YTdlNTgtNWY1MC00NjQ4LWE3Y2MtNjQ2MzYyNGY0ZDBhIiwiaXNzIjoiaHR0cHM6Ly9hcHAuYWphaWIuY28uaWQiLCJpYXQiOjE3ODQ1Mjk0ODgsInN1YiI6IjEzMDAxNjAyMyIsInByaXZhdGVfdXNlcl9pZCI6IjkyNzMwNDlkLTE3NTgtNGNiOS05NDBkLTc2MTlmNWI4MjI1NiIsInNlc3Npb25faWQiOiI1ZjMzNTMxZi00N2NhLTRkNzYtOWNjZi1jOGViNDBlOWI2MmEifQ.ZTzUozUflDKfbrllQ7Bv7xohmTaMT9Nx7dWn69CPOxbOKbqfhitQtuYV6dPZn6f0HXM85ntXDmKOWMhLZtjnVQ"}}
```

### Subscribe:
{"id": 2, "subscribe": {"channel": "margin:coin_futures#130016023"}}

### Protobuf / Json
Centrifugo client can connect to centrifugo server and decide themself to use json or protobuf. To make it easier, you can use json format in wscat, but in your real mobile apps can use protobuf without any issue.

# Dashboard
Open browser, and go to http://localhost:8080/dashboard to open websocket mock dashboard. In our dashboard, there are multiple capabilities:
- Increase rate multiplier
- Monitoring connected event
- Simulate disconnection and peer closed
- Setup new mock in Mock Editor