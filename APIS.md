# APIs & Endpoints

## WebSocket prix BTC (feed/)
- **Binance** : `wss://stream.binance.com:9443/ws/btcusdt@trade`
- **Bitstamp** : `wss://ws.bitstamp.net` — channel `live_trades_btcusd`
- **Coinbase** : `wss://advanced-trade-api.coinbase.com/ws/public`

## Chainlink Oracle (oracle/chainlink.go)
- **Réseau** : Polygon Mainnet
- **Contrat BTC/USD** : `0xc907E116054Ad103354f2D350FD2514433D57F6F`
- Mise à jour : toutes les 10–30s ou déviation > 0.5%

## Polymarket (market/polymarket.go)
- **CLOB** : `https://clob.polymarket.com`
- **Gamma (discovery)** : `https://gamma-api.polymarket.com`

### Contrats Polygon (core)
| Contrat | Adresse |
|---------|---------|
| CTF Exchange | `0xE111180000d2663C0091e4f400237545B87B996B` |
| Neg Risk CTF Exchange | `0xe2222d279d744050d28e00520010520000310F59` |
| Neg Risk Adapter | `0xd91E80cF2E7be2e162c6513ceD06f1dD0dA35296` |
| Conditional Tokens (CTF) | `0x4D97DCd97eC945f40cF65F87097ACe5EA0476045` |
| pUSD CollateralToken (proxy) | `0xC011a7E12a19f7B1f670d46F03B03f3342E82DFB` |
| UMA Adapter | `0x6A9D222616C90FcA5754cd1333cFD9b7fb6a4F74` |

## Polygon RPC
- Alchemy (recommandé) : `https://polygon-mainnet.g.alchemy.com/v2/<KEY>`
- Public (fallback) : `https://polygon-rpc.com`

## Calcul du slug marché (déterministe)

```go
windowTS := time.Now().Unix() - (time.Now().Unix() % 300)
slug := fmt.Sprintf("btc-updown-5m-%d", windowTS)
```
