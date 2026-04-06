```
wscat -c ws://localhost:8080/ws/v3/coin-data/price
wscat -c ws://localhost:8080/ws/v3/coin-data/order-book
wscat -c ws://localhost:8080/ws/v2/coin-data/price
wscat -c ws://localhost:8080/ws/v2/coin-data/order-book
```

list of command
```
Ping
```

```
{
  "stream_type": "PRICE",
  "stream_action": "SUBSCRIBE",
  "source": "COIN_DETAIL",
  "asset_id": "PERP-ETH",
  "id": 1,
  "quote_currency": "IDR"
}
```

```
{
  "stream_type": "ORDER_BOOK",
  "quote_currency": "IDR",
  "asset_id": "PERP-BTC",
  "id": 1,
  "stream_action": "UNSUBSCRIBE"
}
```

```
{"rate": "100"}
```

```
{"close_all_connection": true}
```

```
{"simulate_peer_closed": true}
```