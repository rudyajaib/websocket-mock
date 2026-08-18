# Websocket
- Dashboard:   http://localhost:8080/dashboard
- API Status:  http://localhost:8080/api/status

## Centrifugo Endpoints:
- Public:   ws://localhost:8080/connection/websocket
- Private:  ws://localhost:8080/private/connection/websocket

## Gorilla (old websocket) Endpoints:
- Price V3:  ws://localhost:8080/ws/v3/coin-data/price
- Price V2:  ws://localhost:8080/ws/v2/coin-data/price
- OrderBook: ws://localhost:8080/ws/v3/coin-data/order-book
- Trades:    ws://localhost:8080/ws/coin-data/futures/market-trade
- Watchlist: ws://localhost:8080/ws/v2/watchlist

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
```
wscat -c "wss://edge-events-private.ajaib.tech/connection/websocket?cf_ws_frame_ping_pong=true"
```

### Connect:
```
{"id": 1, "connect": {}}
```

### Connect with token:
```
{"id": 1, "connect": {"token": "eyJ0eXAiOiJQSU4iLCJhbGciOiJFUzI1NiJ9.eyJhamFpYiI6IjEzMDAxNjAyMy5DX0lPUy5QSU4iLCJwbGF0Zm9ybSI6IkNfSU9TIiwiZGV2aWNlX3NpZ25hdHVyZSI6IjA2NjYyNUU4LTExODctNEYxRC05QkYzLTc0MTg0OEVBOTY3QWFybTY0aU9TLXN0YWdpbmciLCJhdWQiOiJodHRwczovL2FwcC5hamFpYi5jby5pZCIsImV4cCI6MTc4Njc4NDc2MywianRpIjoiOWFmMWNjOTctYjcyMy00YjFiLWExMzctNjI1MzA1ZjA5ZjQzIiwiaXNzIjoiaHR0cHM6Ly9hcHAuYWphaWIuY28uaWQiLCJpYXQiOjE3ODY2OTgzNjMsInN1YiI6IjEzMDAxNjAyMyIsInByaXZhdGVfdXNlcl9pZCI6IjkyNzMwNDlkLTE3NTgtNGNiOS05NDBkLTc2MTlmNWI4MjI1NiJ9.SyO7_aOfT8rTHcG27LcAiH_suoihiStFueGTyqS4BEIgroPdZKsv6MXXbEhwnkpirRybTOjVFR2zdnvpRno88Q"}}
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
