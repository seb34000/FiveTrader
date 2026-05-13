# FiveTrader — Bot Polymarket BTC 5min

Bot de trading autonome sur les marchés BTC UP/DOWN 5 minutes de Polymarket.
Go event-driven, goroutines, latence <5ms.

## Stack
- **Go 1.22+** — stdlib + dépendances minimales
- `github.com/gorilla/websocket` — WebSocket Binance/Bitstamp/Coinbase
- `github.com/ethereum/go-ethereum` — EIP-712, transactions Polygon
- `github.com/spf13/viper` / `os.Getenv` — config
- `go.uber.org/zap` — logging structuré
- `github.com/stretchr/testify` — tests

## Architecture
```
main.go                  ← entry point, wiring
config/config.go         ← struct Config + loader .env
feed/
  binance.go             ← WS Binance BTC/USDT
  bitstamp.go            ← WS Bitstamp (cross-validation)
  aggregator.go          ← prix agrégé multi-source
oracle/chainlink.go      ← polling Chainlink BTC/USD Polygon
market/polymarket.go     ← CLOB API : market, orderbook, order
strategy/
  interface.go           ← interface Strategy
  oracle_lag.go          ← Stratégie 1 : Oracle Latency Arbitrage ⭐⭐⭐
  window_delta.go        ← Stratégie 2 : Window Delta T-30s ⭐⭐
  dump_hedge.go          ← Stratégie 3 : Dump & Hedge Arbitrage ⭐⭐
  ensemble.go            ← combinaison pondérée des signaux
risk/manager.go          ← Kelly, circuit breaker, position sizing
execution/executor.go    ← place order, retry, confirmation Polygon
monitor/logger.go        ← P&L, trade log, alertes
```

## Références
- @STRATEGIES.md — détail des 3 stratégies, signal d'entrée, win rate, flux décisionnel
- @RISK.md       — constantes de risque, timing fenêtre 5min
- @APIS.md       — endpoints WebSocket, Chainlink, Polymarket, slug marché
- @WEB.md        — interface web React : setup, pages, API REST, build & embed

## Commandes
```bash
go build -o fivetrader .
./fivetrader --dry-run
./fivetrader --sim
./fivetrader --live
go test ./... -v
```

## Avertissements
- Opère sur Polygon — garder du MATIC pour gas (~0.01 MATIC/tx)
- Ne jamais exposer la clé privée dans les logs
- Commencer en `--sim` minimum 48h avant `--live`
- Le lag oracle peut disparaître si Chainlink améliore sa fréquence

## Dettes connues — P1

- **WinProb non calibrée** — `strategy/oracle_lag.go:138` : `0.72 + …` hardcodé,
  non backtesté. Si le vrai win rate est <0.72, Kelly sursise systématiquement.
  À calibrer sur les données du journal live/sim.
- **Race wallet balance poller** — `wallet_poller.go:66` : `bits.Store(bal)` écrase les
  débits locaux entre deux polls. Impact faible (MaxBetUSDC borne le risque), mais peut
  approuver un trade marginal supplémentaire dans la fenêtre de race (~5-15 s).
- **Tick alignment `buildOrder`** — prix signé arrondi vers le bas dans `execution/executor.go` :
  ratio makerAmount/takerAmount peut diverger du prix réel. À monitorer si nouveaux rejets CLOB.
- **`order_version_mismatch` fix** — `market/polymarket.go:251` : `FeeRateBps=0` dans l'order EIP-712
  (Polymarket applique les fees côté marché, pas dans l'order). Déjà corrigé.

## Pièges & invariants non évidents

- `oracle_lag` bypass `MinEntryPrice`/`MaxEntryPrice` (`risk/manager.go`) — seul `MaxTokenPriceOracleLag=0.92`
  s'applique. Aucune cap absolue en cas de slippage entre signal et fill.
- `convictionScale` (`risk/manager.go`) multiplie Kelly par un facteur lié au prix token :
  tier 0.90→1.0x, tier 0.60→0.3x. Bypassé pour `oracle_lag` et `dump_hedge`.
- Latence "<5 ms" = chemin interne feed→signal uniquement. Le chemin complet tick→ordre
  CLOB est ~85-260 ms (HTTPS). Aucune stratégie temps-réel ne peut cibler <50 ms en live.
- `/api/config` POST (`web/server.go`) réécrit `.env` sans whitelist —
  `PRIVATE_KEY` injectable depuis le navigateur si Basic Auth absent.
- Corrélation ETH/XRP désactivée dans `NewCoordinator` (`main.go`) — risque capé par
  `MaxConcurrentBets=3` + `MaxDailyLoss=$200`. Réactiver si ETH+XRP corrèlent > 0.9 sur 5 min.

## Priorités sessions futures

- **P1** : calibrer `winProb` sur données live · logguer OrderID/TxHash/Slippage · compteur DroppedTicks
- **P2** : orderbook CLOB via WebSocket · Chainlink via events · cache market (TokenID stable 5 min)
- **P3** : whitelist `/api/config` · auth obligatoire si host != 127.0.0.1 · tests feed/ web/ monitor/
