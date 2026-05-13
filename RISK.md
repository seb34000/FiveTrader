# Règles de risque & Timing

## Constantes (risk/manager.go)

> ⚠ Valeurs ci-dessous = code réel exécuté (vérifiées 2026-04-24). Les anciennes valeurs
> documentées (MinEdge=0.04, MinTokenPrice=0.35, MaxTokenPrice=0.75) ne correspondent plus
> au code — voir "Dettes connues" dans CLAUDE.md pour les arbitrages à faire.

```go
// Constantes globales (risk/manager.go:14-16)
const (
    MinEdge          = 0.02  // edge minimum 2% (brut, avant fees Polymarket ~200bps)
    MinEdgeDumpHedge = 0.01  // dump_hedge : seuil réduit (arb risk-free)
)

// Defaults FilterConfig (risk/manager.go:88-107 — surchargeable via config)
MinEntryPrice  = 0.60  // rejeter token < 0.60 (sauf dump_hedge)
MaxEntryPrice  = 0.90  // rejeter token > 0.90 (sauf oracle_lag qui a sa propre cap 0.92)

// Depuis .env / Config struct (valeurs par défaut recommandées)
MaxBetUSDC        = 50.0   // taille max par pari
MaxDailyLossUSDC  = 200.0  // circuit breaker journalier
MaxConcurrentBets = 3      // positions simultanées max
KellyFraction     = 0.25   // quart-Kelly
MaxConsecLosses   = 3      // pause 30min après 3 losses consécutifs
```

**Règle d'or** : si `ask_price > 0.90`, rejeter sauf oracle_lag (cap à 0.92).

**Note fees** : `MinEdge=0.02` est brut. Polymarket peut prélever jusqu'à 200 bps taker.
Edge effectif ≈ edge − FeeRateBps/10000. Le Kelly actuel ne soustrait PAS les fees (dette P0).

## Timing fenêtre 5min

```
T=0s     → ouverture, enregistrer window_open_price
T=0-240s → surveiller oracle lag (stratégie 1)
T=240s   → activer window delta (T-60s)
T=270s   → dernière entrée possible (T-30s)
T=292s   → STOP toute entrée (T-8s, trop tard pour gas Polygon)
T=300s   → expiration, attendre résolution Chainlink
```
