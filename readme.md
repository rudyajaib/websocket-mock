List of API that works with this mock server:
```
ws://localhost:8080/ws/v3/coin-data/price
ws://localhost:8080/ws/v2/coin-data/price
ws://localhost:8080/ws/v3/coin-data/order-book
ws://localhost:8080/ws/v2/coin-data/order-book
ws://localhost:8080/ws/coin-data/futures/market-trade
```

To send command, you can open connection with `wscat`, install it via npm or homebrew.
Example:
```
wscat -c ws://localhost:8080/ws/v3/coin-data/price
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

```
{"order_book_size": "50"}
```