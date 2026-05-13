# FiveTrader — Interface Web React

Dashboard web temps réel pour surveiller et configurer le bot.

## Stack

- **React 18** + **Vite 5** — build rapide, HMR en dev
- **Recharts** — graphiques P&L et prix intégrés React
- **react-router-dom v6** — navigation côté client (hash router)
- Serveur Go existant (`web/server.go`) — WebSocket + API REST

## Démarrage rapide

```bash
# 1. Installer les dépendances
cd web/react && npm install

# 2. Dev (avec proxy vers le bot Go)
npm run dev        # → http://localhost:5173 (proxie :8080)

# 3. Build (embed dans le binaire Go)
npm run build      # → web/static/ (embeddé par go:embed)

# 4. Lancer le bot avec le dashboard
go build -o fivetrader . && ./fivetrader --sim --web
# → http://localhost:8080
```

## Architecture

```
web/
  react/               ← source React (non committé dans le binaire)
    package.json
    vite.config.js
    index.html
    src/
      App.jsx          ← layout + routing (hash-based)
      main.jsx         ← entry point
      styles/
        globals.css    ← variables CSS, reset, composants de base
      hooks/
        useWebSocket.js ← hook WS avec reconnexion automatique
      pages/
        Live.jsx       ← dashboard temps réel (WebSocket)
        History.jsx    ← historique des sessions passées
        Settings.jsx   ← configuration wallet + API + risque
      components/
        Sidebar.jsx    ← navigation latérale
  static/              ← fichiers buildés (embeddés dans le binaire Go)
    index.html
    assets/
      ...
```

## Pages

### Live (`/`)
Données en temps réel via WebSocket (`/ws`) :
- Statistiques globales : P&L total, trades, win rate, perte journalière
- Tabs par asset (BTC, ETH, XRP)
- Prix multi-source (Binance / Bitstamp / Coinbase) + comparaison oracle
- Compteur fenêtre 5min + prix UP/DOWN
- Positions ouvertes + historique récent
- Dernier signal stratégie

### History (`/history`)
Parcourir les sessions passées depuis `sessions/` :
- Liste des sessions avec résumé P&L / win rate
- Filtrage par asset et stratégie
- Graphique P&L cumulé dans le temps
- Tableau des trades avec tous les détails

### Settings (`/settings`)
Modifier `.env` sans redémarrer le terminal :
- Wallet : clé privée (masquée), adresse dérivée
- Polymarket : API key / secret / passphrase
- Réseau : Polygon RPC
- Risque : MaxBet, MaxDailyLoss, ConcurrentBets, KellyFraction
- Stratégies : toggles oracle_lag / window_delta / dump_hedge
> **Note** : Les changements nécessitent un redémarrage du bot pour être pris en compte.

## API REST (Go)

| Méthode | Endpoint | Description |
|---------|----------|-------------|
| `GET` | `/ws` | WebSocket live data |
| `GET` | `/api/sessions` | Liste des sessions avec stats |
| `GET` | `/api/session-trades?session=NAME&asset=btc` | Trades d'une session |
| `GET` | `/api/config` | Config actuelle (valeurs sensibles masquées) |
| `POST` | `/api/config` | Mettre à jour `.env` |

## Build & Embed

Le Vite est configuré pour output dans `web/static/` avec `emptyOutDir: true`.
Le Go embed (`//go:embed static`) prend tout ce dossier.

Workflow de release :
```bash
cd web/react && npm run build  # compile React → web/static/
cd ../.. && go build -o fivetrader .  # embed + compile Go
```
