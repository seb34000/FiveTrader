# FiveTrader — Guide de mise en route

## Table des matières
1. [Créer un wallet dédié](#1-créer-un-wallet-dédié)
2. [Configurer le fichier .env](#2-configurer-le-fichier-env)
3. [Obtenir des credentials Polymarket](#3-obtenir-des-credentials-polymarket)
4. [Tester en dry-run (sans argent réel)](#4-tester-en-dry-run-sans-argent-réel)
5. [Vérifier les feeds de prix](#5-vérifier-les-feeds-de-prix)
6. [Alimenter le wallet & déposer sur Polymarket](#6-alimenter-le-wallet--déposer-sur-polymarket)
7. [Lancer en mode live](#7-lancer-en-mode-live)
8. [Surveillance & logs](#8-surveillance--logs)
9. [Checklist avant le premier trade réel](#9-checklist-avant-le-premier-trade-réel)

---

## 1. Créer un wallet dédié

**IMPORTANT** : N'utilise JAMAIS ton wallet principal. Crée un wallet dédié uniquement pour ce bot.

### Option A — MetaMask (recommandé pour débutants)
1. Installe [MetaMask](https://metamask.io) (extension Chrome/Firefox)
2. Crée un nouveau compte → "Créer un portefeuille"
3. Note ta **seed phrase** de 12 mots dans un endroit sécurisé (jamais en ligne)
4. Exporte la clé privée : Paramètres → Compte → Exporter la clé privée
5. La clé privée est une chaîne hex de 64 caractères, ex: `0xabc123...`

### Option B — Cast (CLI, plus sécurisé)
```bash
# Installer foundry (inclut cast)
curl -L https://foundry.paradigm.xyz | bash
foundryup

# Générer un nouveau wallet
cast wallet new
# Output:
#   Address:     0xYourAddress
#   Private key: 0xYourPrivateKey
```

### Option C — Python (si tu préfères)
```bash
pip install eth-account
python3 -c "
from eth_account import Account
import secrets
key = '0x' + secrets.token_hex(32)
acct = Account.from_key(key)
print('Address:    ', acct.address)
print('Private key:', key)
"
```

> **Sécurité** : La clé privée ne doit jamais apparaître dans les logs, Git, ou être partagée.
> Le bot lit `PRIVATE_KEY` depuis `.env` — ce fichier est dans `.gitignore`.

---

## 2. Configurer le fichier .env

```bash
cp .env.example .env
```

Édite `.env` :

```env
# Ton wallet dédié (clé privée complète avec 0x)
PRIVATE_KEY=0xTaCléPrivéeIci

# Polymarket API (voir section 3)
POLY_API_KEY=
POLY_API_SECRET=
POLY_API_PASSPHRASE=

# RPC Polygon — utilise Alchemy pour fiabilité
POLYGON_RPC=https://polygon-mainnet.g.alchemy.com/v2/TON_API_KEY_ALCHEMY

# Mode sécurisé : toujours commencer en dry-run
DRY_RUN=true

# Risque (ne pas toucher avant d'avoir validé en paper)
MAX_BET_USDC=50
MAX_DAILY_LOSS_USDC=200
MAX_CONCURRENT_BETS=3
KELLY_FRACTION=0.25

# Stratégies actives
ENABLE_ORACLE_LAG=true
ENABLE_WINDOW_DELTA=true
ENABLE_DUMP_HEDGE=true
```

### Obtenir un RPC Alchemy (recommandé)
1. Crée un compte sur [alchemy.com](https://alchemy.com)
2. Crée une app → Network: **Polygon Mainnet**
3. Copie l'HTTPS endpoint → `POLYGON_RPC`

> Le RPC public `https://polygon-rpc.com` fonctionne mais est souvent lent/instable.
> Alchemy offre 300M unités/mois gratuitement, largement suffisant.

---

## 3. Obtenir des credentials Polymarket

Polymarket utilise un système de clés API L2 (pas ton wallet directement).

### Via l'interface web (le plus simple)
1. Va sur [polymarket.com](https://polymarket.com)
2. Connecte ton wallet MetaMask (le wallet dédié créé en étape 1)
3. Va dans **Settings → API Keys → Create API Key**
4. Note les 3 valeurs : `API Key`, `Secret`, `Passphrase`

### Via py-clob-client (CLI)
```bash
pip install py-clob-client

python3 -c "
from py_clob_client.client import ClobClient
from py_clob_client.constants import POLYGON

client = ClobClient(
    host='https://clob.polymarket.com',
    chain_id=POLYGON,
    private_key='0xTaCléPrivée',
)
creds = client.create_api_key()
print('API Key:   ', creds.api_key)
print('Secret:    ', creds.api_secret)
print('Passphrase:', creds.api_passphrase)
"
```

> Les clés API Polymarket sont liées à ton adresse wallet.
> Elles n'expirent pas mais peuvent être révoquées via l'interface web.

---

## 4. Tester en dry-run puis en simulation

Il y a trois modes de fonctionnement :

| Mode | Commande | Ordres réels | P&L tracké | Journal |
|------|----------|:---:|:---:|:---:|
| **Dry-run** | `--dry-run` | Non | Non | Non |
| **Sim** | `--sim` | Non | Oui | `sim_journal_*.jsonl` |
| **Live** | `--live` | Oui | Oui | — |

### Dry-run (aucun argent, aucun suivi)

Simule tout le pipeline sans placer d'ordres ni calculer de P&L.
`PRIVATE_KEY` est nécessaire (pour dériver l'adresse) mais pas les clés API.

```bash
# Compiler
go build -o fivetrader .

# Lancer en dry-run
./fivetrader --dry-run
```

### Sim — simulation live avec P&L réel (recommandé avant le live)

Le mode `--sim` utilise les vrais flux de prix (Binance/Bitstamp/Chainlink) et simule
les fills et le P&L sans risquer d'argent. Un journal JSONL est écrit à la fin de chaque session.

```bash
./fivetrader --sim

# Avec TUI (terminal dashboard)
./fivetrader --sim --ui

# Lire le journal après la session
cat sim_journal_*.jsonl | jq .
```

**Durée recommandée** : **minimum 48h de sim** avec des résultats positifs avant de passer en live.

**Ce que tu dois voir dans les logs :**
```
INFO  === DRY-RUN MODE — no real orders will be placed ===
INFO  wallet loaded   {"address": "0xTonAdresse"}
INFO  new window      {"start": "...", "btc_open": 87450.12}
INFO  price tick      {"live": 87451.30, "oracle": 87445.00, "oracle_age_s": 12}
INFO  signal          {"strategy": "oracle_lag", "direction": "UP", "edge": 0.062}
INFO  [DRY-RUN] order skipped  {"size_usdc": 23.50}
```

**Durée recommandée** : **minimum 48h** de dry-run avant tout trade réel.

Surveille :
- Les feeds Binance/Bitstamp/Coinbase se connectent bien (reconnexion automatique)
- L'oracle Chainlink poll toutes les **~5 secondes** (le poller interne), mais Chainlink lui-même se met à jour toutes les 10–30s
- Les signaux `oracle_lag` apparaissent (quelques fois par heure en période volatile)
- Aucune panique / crash sur 48h

---

## 5. Vérifier les feeds de prix

Pour débugger un feed spécifique sans lancer tout le bot :

```bash
# Test WebSocket Binance (manuel)
wscat -c "wss://stream.binance.com:9443/ws/btcusdt@trade" | head -5

# Test oracle Chainlink (lecture contrat)
cast call 0xc907E116054Ad103354f2D350FD2514433D57F6F \
  "latestRoundData()(uint80,int256,uint256,uint256,uint80)" \
  --rpc-url https://polygon-rpc.com

# Vérifier que le prix retourné est cohérent (diviser par 1e8)
```

---

## 6. Alimenter le wallet & déposer sur Polymarket

### Étape 1 — Obtenir du MATIC (gas Polygon)
- Besoin : ~0.5 MATIC pour démarrer (chaque tx coûte ~0.01 MATIC)
- Achète sur Binance/Coinbase et withdraw vers ton adresse Polygon
- Ou bridge depuis Ethereum via [polygon.technology/bridge](https://polygon.technology/bridge)

### Étape 2 — Obtenir de l'USDC sur Polygon
- Achète de l'USDC sur Binance, withdraw vers **Polygon network** (pas Ethereum !)
- Ou swap sur Uniswap/QuickSwap sur Polygon

### Étape 3 — Déposer sur Polymarket
1. Va sur [polymarket.com](https://polymarket.com) avec ton wallet connecté
2. **Deposit** → entre le montant USDC
3. Polymarket convertit en USDC.e (bridged USDC) utilisé pour les paris

> **Budget recommandé pour débuter** :
> - 0.5 MATIC pour le gas (~5$ suffisent pour des centaines de tx)
> - 100-200 USDC pour tester (le bot peut perdre `MAX_DAILY_LOSS_USDC`)

---

## 7. Lancer en mode live

**Ne faire ceci qu'après 48h de sim validé (P&L positif, pas de crash).**

```bash
# Édite .env : mettre DRY_RUN=false et remplir les clés API Polymarket
nano .env

# Lancer
./fivetrader --live

# Ou
DRY_RUN=false ./fivetrader
```

### Lancer en arrière-plan avec tmux (recommandé)
```bash
# Installer tmux si pas présent
brew install tmux

# Créer une session persistante
tmux new -s fivetrader

# Dans la session tmux
./fivetrader --live 2>&1 | tee -a trader.log

# Détacher sans tuer : Ctrl+B puis D
# Réattacher plus tard : tmux attach -t fivetrader
```

### Avec nohup (alternative simple)
```bash
nohup ./fivetrader --live >> trader.log 2>&1 &
echo $! > trader.pid   # sauvegarder le PID
tail -f trader.log      # suivre les logs en temps réel
```

---

## 8. Surveillance & logs

```bash
# Suivre les logs en temps réel
tail -f trader.log

# Chercher les trades exécutés
grep "executing" trader.log

# Chercher les erreurs
grep "ERROR\|FATAL" trader.log

# Chercher les stats P&L (toutes les 5 minutes)
grep "stats" trader.log

# Arrêter proprement le bot
kill -TERM $(cat trader.pid)
# ou Ctrl+C si en foreground / tmux
```

**Signaux d'alerte à surveiller :**
- `ERROR execution failed` → problème d'ordre, vérifier balance USDC / clés API
- `WARN circuit breaker` → perte journalière atteinte, bot s'arrête automatiquement
- `ERROR websocket` → reconnexion en cours (normal si réseau instable)
- `FATAL` → crash critique, relancer manuellement

---

## 9. Checklist avant le premier trade réel

```
[ ] Wallet dédié créé (jamais utilisé ailleurs)
[ ] Clé privée sauvegardée hors ligne (papier ou gestionnaire de mots de passe)
[ ] .env configuré et POLYGON_RPC = endpoint Alchemy (pas le public)
[ ] Clés API Polymarket renseignées (key + secret + passphrase)
[ ] 48h de --sim sans crash, P&L positif sur le journal sim_journal_*.jsonl
[ ] Signaux oracle_lag et/ou dump_hedge observés dans les logs de sim
[ ] Wallet alimenté : MATIC (gas) + USDC (mise)
[ ] USDC déposé sur Polymarket
[ ] MAX_BET_USDC conservateur (ex: 10$ pour débuter, pas 50$)
[ ] MAX_DAILY_LOSS_USDC = budget que tu acceptes de perdre en une journée
[ ] tmux ou nohup configuré pour ne pas perdre le process
[ ] Première session live supervisée manuellement (ne pas laisser tourner seul le 1er jour)
```

---

## Résumé des commandes clés

```bash
go build -o fivetrader .          # Compiler
./fivetrader --dry-run             # Dry-run (feeds réels, pas de P&L)
./fivetrader --sim                 # Simulation (feeds réels, P&L tracké, journal JSONL)
./fivetrader --sim --ui            # Simulation avec dashboard terminal
./fivetrader --live                # Trading réel
kill -TERM $(cat trader.pid)       # Arrêt propre
tail -f trader.log                 # Suivre les logs
grep "stats" trader.log            # P&L périodique
cat sim_journal_*.jsonl | jq .     # Lire le journal de simulation
```
