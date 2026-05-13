# Comment le bot gagne de l'argent

FiveTrader est un bot de trading sur **Polymarket**, une plateforme de marchés prédictifs.
Il joue sur un seul type de marché : **"BTC UP/DOWN 5 minutes"**.

---

## Le marché sur lequel on joue

Toutes les 5 minutes, Polymarket ouvre un nouveau marché binaire :

> **"Le prix du Bitcoin sera-t-il plus haut ou plus bas dans 5 minutes ?"**

Il y a deux tokens :
- **UP** — vaut $1 si BTC finit plus haut, $0 sinon
- **DOWN** — vaut $1 si BTC finit plus bas, $0 sinon

Le marché se résout sur le **prix Chainlink oracle** exactement à l'expiration.

### Exemple concret

```
Token UP coûte $0.58 → si tu gagnes, tu récupères $1.00
                      → profit = $1.00 - $0.58 = $0.42 par dollar
                      → rendement = +72% en 5 minutes
```

C'est un pari binaire pur : **tout ou rien** à chaque fenêtre.

---

## Stratégie 1 — Oracle Latency Arbitrage ⭐⭐⭐

**C'est la stratégie principale. Win rate estimé : 70–90%.**

### Le principe

Polymarket se résout sur le **prix Chainlink** (un oracle blockchain).
Chainlink met à jour son prix toutes les **10–30 secondes**, ou quand le BTC bouge de plus de 0.5%.

Le bot surveille le prix BTC en **temps réel** via Binance, Bitstamp et Coinbase (WebSocket, latence < 50ms).

Si le BTC vient de bouger fortement et que **Chainlink n'a pas encore mis à jour**, il y a un écart :

```
BTC live    = $105,000   ← ce que les exchanges voient maintenant
BTC oracle  = $104,650   ← ce que Chainlink va déclarer à l'expiration

Écart = +0.33%  →  BTC a monté, oracle pas encore à jour
```

Dans ce cas, le marché UP est **sous-évalué** : les traders ne savent pas encore que l'oracle va monter. Le bot achète UP **avant** que le marché corrige.

### Signal d'entrée

```
delta = (prix_live - prix_oracle) / prix_oracle

Si delta > +0.15%  →  acheter UP   (BTC monté, oracle en retard)
Si delta < -0.15%  →  acheter DOWN (BTC baissé, oracle en retard)
```

> `MinLagThreshold = 0.0015` dans `strategy/oracle_lag.go:12`.

### Conditions de validité

| Condition | Valeur | Raison |
|-----------|--------|--------|
| Âge minimum du lag | 3 secondes | Éviter les faux positifs juste après une mise à jour |
| Âge maximum du lag | 2 minutes | Au-delà, les données sont peu fiables |
| Prix token maximum | $0.92 | Si > $0.92, le marché a déjà pricé le lag |
| Temps restant | > 5 secondes | Pas assez de temps pour exécuter |

### Pourquoi ça marche

Chainlink a une **latence structurelle**. Polymarket résout sur Chainlink, pas sur Binance.
Les 5–45 secondes entre le mouvement réel du BTC et la mise à jour de l'oracle créent une fenêtre où le marché est systématiquement mal pricé.

---

## Stratégie 2 — Window Delta T-60s ⭐⭐

**Win rate estimé : 55–62%.**

### Le principe

À **60 secondes avant la clôture** d'une fenêtre, la direction est quasi-décidée.
Le delta depuis l'ouverture de la fenêtre est le signal le plus prédictif.

```
window_open = prix BTC au début de la fenêtre (toutes les 5 min)
delta = (prix_actuel - window_open) / window_open

Si BTC a monté de +0.1% depuis l'ouverture → BET UP  (si token UP < $0.78)
Si BTC a baissé de -0.1% depuis l'ouverture → BET DOWN (si token DOWN < $0.78)
```

> `WindowDeltaEntryT = 240.0` et `MaxEntryTokenPrice = 0.78` dans `strategy/window_delta.go:12-13`.

### Fenêtre d'entrée

```
Fenêtre ouverte à T=0s
├── T=0 à T=240s  → surveiller, ne pas entrer encore
├── T=240s (T-60s) → ENTRÉE POSSIBLE  ← on commence à chercher
├── T=292s (T-8s)  → DERNIÈRE ENTRÉE  ← trop tard ensuite (gas Polygon)
└── T=300s         → EXPIRATION
```

### Courbe de probabilité

| Delta depuis ouverture | Win rate estimé | Token attendu |
|------------------------|-----------------|---------------|
| < 0.05% | ~50% (coin flip) | ~$0.50 |
| ~0.10% | ~65% | ~$0.55 |
| ~0.20% | ~80% | ~$0.65 |
| ~0.30% | ~92% | ~$0.75 |

On n'entre que si le token coûte **moins de $0.78** — cela garantit un edge positif.

### Pourquoi ça marche

Psychologie de marché : à T-30s, la plupart des participants ont déjà pris position.
Le carnet d'ordres se fige. Si la tendance est claire depuis l'ouverture, elle a statistiquement plus de chances de se confirmer que de s'inverser dans les 30 dernières secondes.

---

## Stratégie 3 — Dump & Hedge Arbitrage ⭐⭐

**Win rate : 100% — profit garanti. Rare mais sans risque.**

### Le principe

Normalement, sur un marché binaire équilibré :

```
prix_UP + prix_DOWN ≈ $1.00
```

Parfois, suite à un mouvement brutal ("dump"), un des côtés chute violemment.
Si UP tombe à $0.44 et DOWN à $0.49 :

```
UP ($0.44) + DOWN ($0.49) = $0.93   →  décote de 7%
```

Le bot **achète un nombre égal de tokens** sur les deux côtés simultanément.

À l'expiration, **un des deux vaut $1.00** (l'un gagne forcément) — et la paire de N tokens rapporte toujours N dollars, quelle que soit la direction.

### Signal d'entrée

```
Si askUP + askDOWN < $0.98 → arbitrage déclenché
PnL = budget × (1 / sum − 1)   ← toujours positif quand sum < 1
```

> `MaxSumForArb = 0.98` dans `strategy/dump_hedge.go:7`.
> ⚠ À 0.98 brut, les fees Polymarket (~200 bps) mangent ~2% d'edge — revoir si non rentable.

### Exemple (sizing token-based)

```
askUP   = $0.44
askDOWN = $0.49
sum     = $0.93    →  arbitrage déclenché

Budget alloué = $10.00 USDC
N tokens = $10.00 / $0.93 = 10.75 tokens de chaque côté

  Coût UP   = 10.75 × $0.44 = $4.73
  Coût DOWN = 10.75 × $0.49 = $5.27
  Total     = $10.00

À l'expiration (peu importe la direction) :
  Payout = 10.75 tokens × $1.00 = $10.75
  PnL    = $10.75 − $10.00 = +$0.75  (+7.5% garanti)
```

> **Pourquoi le sizing en tokens est critique** : avec un montant USDC égal sur chaque jambe, le payout dépend du côté gagnant et peut être inférieur au coût total si l'un des prix est > $0.50. Le sizing en tokens garantit un payout identique dans les deux cas.

Ce signal a **Confidence = 1.0** et **WinProb = 100%** — aucune incertitude.

---

## Gestion du risque — Le garde-fou

Le bot ne joue **jamais** sans validation par le gestionnaire de risque.

### Règles appliquées à chaque signal

| Règle | Valeur par défaut | Description |
|-------|-------------------|-------------|
| Edge minimum | 4% | Le signal doit offrir au moins 4% d'avantage théorique |
| Prix token minimum | $0.35 | Un token trop bon marché = risque extrême |
| Prix token maximum | $0.75 | Sauf oracle_lag et dump_hedge |
| Positions simultanées max | 3 | Évite la surexposition |
| Perte journalière max | $200 | Circuit breaker : arrêt total si atteint |

### Sizing Kelly

Le bot ne mise jamais un montant fixe. Il utilise la **formule de Kelly fractionnelle** pour calculer la mise optimale :

```
Kelly = (p × (b+1) - 1) / b

où :
  p = probabilité de gagner (estimée par la stratégie)
  b = cote nette = (1 / prix_token) - 1

Mise finale = Kelly × 0.25 × MaxBet
             (quart-Kelly pour réduire la variance)
```

**Exemple :**
```
Token UP = $0.60, WinProb = 78%
b = (1/0.60) - 1 = 0.667
Kelly = (0.78 × 1.667 - 1) / 0.667 = 0.627
Mise = 0.627 × 0.25 × $50 = $7.84
```

Le Kelly fractionnel (×0.25) est une protection contre les estimations imparfaites de winProb.

---

## Flux d'une décision

```
Toutes les ~100ms :

1. Prix BTC agrégé (Binance + Bitstamp + Coinbase)
   ↓
2. Évaluation des 3 stratégies (priorité: DumpHedge > OracleLag > WindowDelta)
   ↓
3. Un signal est généré ? → Validation risque (edge, prix, positions, perte)
   ↓
4. Kelly sizing → taille de mise calculée
   ↓
5. Ordre envoyé sur Polymarket CLOB (ou simulé en mode --sim)
   ↓
6. Résolution à l'expiration (prix Chainlink final)
   ↓
7. P&L calculé et enregistré
```

---

## Pourquoi ce marché spécifiquement

| Avantage | Détail |
|----------|--------|
| **Inefficience structurelle** | L'oracle Chainlink a une latence exploitable en permanence |
| **Haute fréquence** | Une nouvelle fenêtre toutes les 5 minutes = 288 opportunités/jour |
| **Marché profond** | Les marchés BTC 5min ont de la liquidité (spreads serrés) |
| **Résolution déterministe** | Pas de subjectivité — Chainlink décide, pas des humains |
| **Pas de contrepartie risquée** | C'est un CLOB automatisé, pas OTC |

---

## Ce que le bot ne fait PAS

- Il ne prédit pas le futur — il **exploite des inefficiences de prix présentes**
- Il ne trade pas sur des opinions ou actualités
- Il ne garde jamais de position overnight (toutes les fenêtres expirent dans la journée)
- Il ne risque jamais plus de $200/jour (circuit breaker)

---

## Résumé des rendements attendus

| Stratégie | Fréquence | Win Rate | Rendement/trade |
|-----------|-----------|----------|-----------------|
| Oracle Lag | 5–15×/heure | 70–90% | Variable (dépend du prix token) |
| Window Delta | 0–3×/heure | 55–62% | Variable |
| Dump Hedge | Rare (1–5×/jour) | 100% | +4 à +10% garanti |

> **Avertissement** : Ces chiffres sont des estimations basées sur la logique des stratégies et des données historiques partielles. Les performances passées ne garantissent pas les performances futures. Commencer toujours en mode `--sim` (simulation) avant de trader en live.
