# Polymarket BTC 5min Bot — Go

## Contexte
Bot de trading autonome sur les marchés BTC UP/DOWN 5 minutes de Polymarket.
Écrit en **Go** pour la performance maximale (event-driven, goroutines, latence <5ms).

## Stack
- **Go 1.22+** — pas de frameworks, stdlib + dépendances minimales
- **WebSocket** : `github.com/gorilla/websocket` — flux Binance/Bitstamp/Coinbase temps réel
- **HTTP** : `net/http` stdlib — appels CLOB Polymarket + Chainlink oracle
- **Crypto/Web3** : `github.com/ethereum/go-ethereum` — signature EIP-712, transactions Polygon
- **Config** : `github.com/spf13/viper` ou simple `os.Getenv` + struct
- **Logging** : `go.uber.org/zap` — structured logging haute performance
- **Tests** : `testing` stdlib + `github.com/stretchr/testify`

## Architecture
```
polymarket-btc-bot/
├── CLAUDE.md
├── .claude/
│   ├── settings.json
│   └── settings.local.json
├── .env.example
├── go.mod
├── go.sum
├── main.go                        ← entry point, wiring
├── config/
│   └── config.go                  ← struct Config + loader .env
├── feed/
│   ├── binance.go                 ← WebSocket Binance BTC/USDT ticks
│   ├── bitstamp.go                ← WebSocket Bitstamp (cross-validation)
│   └── aggregator.go              ← prix agrégé multi-source
├── oracle/
│   └── chainlink.go               ← polling Chainlink BTC/USD sur Polygon
├── market/
│   └── polymarket.go              ← CLOB API : find market, orderbook, place order
├── strategy/
│   ├── interface.go               ← interface Strategy
│   ├── oracle_lag.go              ← [STRATÉGIE 1] Oracle Latency Arbitrage
│   ├── window_delta.go            ← [STRATÉGIE 2] Window Delta (T-10s entry)
│   ├── dump_hedge.go              ← [STRATÉGIE 3] Dump & Hedge arbitrage
│   └── ensemble.go                ← combinaison pondérée des signaux
├── risk/
│   └── manager.go                 ← Kelly, circuit breaker, position sizing
├── execution/
│   └── executor.go                ← place order, retry, confirmation Polygon
└── monitor/
    └── logger.go                  ← P&L, trade log, alertes
```

---

## Stratégies implémentées (par ordre de priorité)

### STRATÉGIE 1 — Oracle Latency Arbitrage ⭐⭐⭐ (PRIORITÉ HAUTE)
**Source** : validée par PolyCryptoBot (Go), bots Phemex ($5-10k/jour rapportés)

**Principe** :
Chainlink met à jour son oracle BTC/USD toutes les **10–30 secondes** ou sur déviation >0.5%.
Polymarket résout le marché sur le snapshot Chainlink à l'expiration exacte.
→ Si BTC bouge sur Binance/Bitstamp **AVANT** que Chainlink mette à jour,
  il y a une fenêtre de 5–45 secondes où le marché Polymarket est **mal pricé**.

**Signal d'entrée** :
```
btc_live_price = moyenne(binance_ws, bitstamp_ws, coinbase_ws)
btc_oracle_price = dernière valeur Chainlink connue
delta = (btc_live_price - btc_oracle_price) / btc_oracle_price

SI delta > +0.003 → BTC a monté, oracle pas encore à jour → BET UP
SI delta < -0.003 → BTC a baissé, oracle pas encore à jour → BET DOWN
```

**Fenêtre d'entrée** : dès détection du lag, jusqu'à T-5s avant expiration
**Win rate estimé** : 70–90% (conditionnel à la détection d'un vrai lag)
**Fréquence** : 5–15 trades/heure selon volatilité

**Implémentation Go** :
```go
type OracleLagSignal struct {
    LivePrice    float64
    OraclePrice  float64
    DeltaPct     float64        // (live - oracle) / oracle
    Direction    int            // +1 UP, -1 DOWN
    Confidence   float64        // abs(delta) normalisé [0,1]
    LagDetectedAt time.Time
}

const MinLagThreshold = 0.003            // 0.3% minimum pour agir
const MinLagAgeSec   = 3 * time.Second  // ignorer si oracle vient juste de se mettre à jour
const MaxLagAgeSec   = 120 * time.Second // ignorer lag > 120s (données peu fiables)
```

---

### STRATÉGIE 2 — Window Delta T-10s ⭐⭐ (PRIORITÉ MOYENNE)
**Source** : validée empiriquement (GitHub Archetapp gist, 55-60% win rate OOS)

**Principe** :
À T-10 secondes avant la clôture, la direction est quasi-lockée.
Le delta `(prix_actuel - prix_ouverture_fenêtre) / prix_ouverture_fenêtre`
est le signal le plus prédictif pour une fenêtre 5min binaire.
On entre sur le côté qui a déjà gagné si le token est encore sous-pricé.

**Signal d'entrée** :
```
window_open = prix BTC au début de la fenêtre (timestamp % 300)
window_delta = (btc_now - window_open) / window_open

SI window_delta > +0.001 ET token UP < 0.72 → BET UP (sous-pricé)
SI window_delta < -0.001 ET token DOWN < 0.72 → BET DOWN (sous-pricé)
```

**Timing** : entre T-30s et T-8s (pas trop tôt = incertitude, pas trop tard = gas)
**Win rate estimé** : 55–62% (dépend du seuil de cote)
**Pricing model** : cote observée sur le marché suit le delta (voir config)

```
delta < 0.005% → cote ~$0.50 (coin flip)
delta ~ 0.02%  → cote ~$0.55
delta ~ 0.05%  → cote ~$0.65
delta ~ 0.10%  → cote ~$0.80
delta ~ 0.15%+ → cote ~$0.92
```
→ **N'entrer que si le token coûte < 0.75** (edge positif requis)

---

### STRATÉGIE 3 — Dump & Hedge Arbitrage ⭐⭐ (PRIORITÉ MOYENNE)
**Source** : GitHub dev-protocol/polymarket-arbitrage-bot

**Principe** :
Si UP + DOWN tokens < $1.00, acheter les deux = profit garanti à résolution.
On n'agit que quand la décote est significative (sum < $0.96).

**Signal** :
```
sum = ask_UP + ask_DOWN
SI sum < 0.96 → arbitrage déclenché
```

**Sizing — token-based (critique)** :
Le bot achète un nombre **égal de tokens** sur chaque côté, pas un montant USDC égal.
```
budget = Kelly × MaxBet           // ex: $10 USDC total
N      = budget / sum             // ex: 10 / 0.93 = 10.75 tokens
coût_UP   = N × ask_UP            // ex: 10.75 × 0.44 = $4.73
coût_DOWN = N × ask_DOWN          // ex: 10.75 × 0.49 = $5.27
payout    = N × $1.00             // ex: $10.75 (indépendant de la direction)
PnL       = budget × (1/sum - 1)  // ex: +$0.75 = +7.5% garanti
```

Avec un sizing USDC égal (misère × 2), le profit dépend du côté gagnant et peut être négatif si l'un des prix > 0.50. Le sizing token-based garantit `payout = N` quelle que soit l'issue.

**Fréquence** : rare mais risque-free
**Condition** : nécessite liquidité des deux côtés simultanément

Le signal expose `AskPrice = sum` (prix total d'une paire de tokens) et `AskPriceDown = ask_DOWN`.
L'executor en déduit `ask_UP = AskPrice - AskPriceDown` pour chaque jambe.

---

### STRATÉGIE 4 — Order Book Imbalance ⭐ (PRIORITÉ BASSE)
**Principe** : déséquilibre bid/ask sur Binance prédit la direction courte.
```
imbalance = bid_volume_top5 / (bid_volume_top5 + ask_volume_top5)
SI imbalance > 0.65 → UP
SI imbalance < 0.35 → DOWN
```
Combiné avec momentum (ROC 1min > 0) pour filtrer.

---

## Règles de risque — NON NÉGOCIABLES
```go
const (
    MaxBetUSDC         = 50.0    // taille max par pari
    MaxDailyLossUSDC   = 200.0   // circuit breaker journalier
    MaxConcurrentBets  = 3       // positions simultanées
    MinEdge            = 0.04    // edge minimum 4%
    MinTokenPrice      = 0.35    // ne pas acheter si token < 0.35
    MaxTokenPrice      = 0.75    // ne pas acheter si token > 0.75 (sauf oracle lag)
    KellyFraction      = 0.25    // quart-Kelly
)
```

**Règle d'or** : si `ask_price > 0.75`, ne pas entrer sauf si oracle lag confirmé.

## Timing critique
```
T=0s    → ouverture fenêtre, enregistrer window_open_price
T=0-240s → surveiller oracle lag (stratégie 1)
T=240s  → activer stratégie window delta (T-60s)
T=270s  → dernière entrée possible (T-30s)
T=292s  → STOP toute entrée (T-8s, trop tard pour gas Polygon)
T=300s  → expiration, attendre résolution Chainlink
```

## APIs utilisées
- **Binance WebSocket** : `wss://stream.binance.com:9443/ws/btcusdt@trade`
- **Bitstamp WebSocket** : `wss://ws.bitstamp.net` channel `live_trades_btcusd`
- **Coinbase WebSocket** : `wss://advanced-trade-api.coinbase.com/ws/public`
- **Chainlink Oracle** (Polygon) : contract `0xc907E116054Ad103354f2D350FD2514433D57F6F` (BTC/USD)
- **Polymarket CLOB** : `https://clob.polymarket.com`
- **Polymarket Gamma** : `https://gamma-api.polymarket.com` (discovery marchés)
- **Polygon RPC** : Alchemy ou `https://polygon-rpc.com`

## Calcul du slug marché (déterministe)
```go
// Les marchés 5min suivent les timestamps Unix divisibles par 300
windowTS := time.Now().Unix() - (time.Now().Unix() % 300)
slug := fmt.Sprintf("btc-updown-5m-%d", windowTS)
```

## Commandes de dev
```bash
go build ./...
go test ./... -v
go run main.go --dry-run
go run main.go --strategy oracle_lag --dry-run
go run main.go --live
```

## Avertissements
- Le bot opère sur Polygon — garder du MATIC pour le gas (~0.01 MATIC/tx)
- Le lag oracle peut disparaître si Chainlink améliore sa fréquence
- Ne jamais exposer la clé privée dans les logs
- Commencer en paper mode (DRY_RUN=true) minimum 48h