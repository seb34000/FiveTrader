package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/seb/fivetrader/asset"
	"github.com/seb/fivetrader/config"
	"github.com/seb/fivetrader/execution"
	"github.com/seb/fivetrader/feed"
	"github.com/seb/fivetrader/market"
	"github.com/seb/fivetrader/monitor"
	"github.com/seb/fivetrader/oracle"
	"github.com/seb/fivetrader/risk"
	"github.com/seb/fivetrader/sim"
	"github.com/seb/fivetrader/strategy"
	"github.com/seb/fivetrader/ui"
	"github.com/seb/fivetrader/web"
)

// botState merges per-asset states into a single ui.Update, thread-safe.
type botState struct {
	mu     sync.Mutex
	update ui.Update
}

func (s *botState) init(mode, address string) {
	s.mu.Lock()
	s.update = ui.Update{
		Mode:    mode,
		Address: address,
		Assets:  make(map[string]ui.AssetState),
	}
	s.mu.Unlock()
}

func (s *botState) setAsset(as ui.AssetState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.update.Assets[as.Symbol] = as
	// Recompute aggregated totals
	var totalPnL, totalDailyLoss float64
	var totalTrades int
	var weightedWins float64
	for _, a := range s.update.Assets {
		totalPnL += a.PnL
		totalDailyLoss += a.DailyLoss
		totalTrades += a.SettledTrades
		weightedWins += a.WinRate * float64(a.SettledTrades)
	}
	s.update.TotalPnL = totalPnL
	s.update.TotalDailyLoss = totalDailyLoss
	s.update.TotalTrades = totalTrades
	if totalTrades > 0 {
		s.update.TotalWinRate = weightedWins / float64(totalTrades)
	}
}

func (s *botState) snapshot() ui.Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.update
	// Deep-copy the assets map so the caller owns it
	cp := make(map[string]ui.AssetState, len(u.Assets))
	for k, v := range u.Assets {
		cp[k] = v
	}
	u.Assets = cp
	return u
}

func main() {
	var (
		dryRun   = flag.Bool("dry-run", false, "Simulate without placing real orders (P&L not tracked)")
		simMode  = flag.Bool("sim", false, "Live simulation: real feeds, simulated fills, P&L tracked")
		liveMode = flag.Bool("live", false, "Enable live trading (requires API credentials)")
		force    = flag.Bool("force", false, "Skip LIVE mode confirmation prompt")
		webMode  = flag.Bool("web", false, "Enable web dashboard")
		webPort  = flag.Int("web-port", 8080, "Port for the web dashboard")
		assets   = flag.String("assets", "btc,eth,xrp", "Comma-separated list of assets to trade")
	)
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	if *dryRun {
		cfg.Mode = config.ModeDryRun
	}
	if *simMode {
		cfg.Mode = config.ModeSim
	}
	if *liveMode {
		cfg.Mode = config.ModeLive
	}

	log, err := monitor.NewLogger(cfg.Mode)
	if err != nil {
		panic(err)
	}
	defer log.Sync()

	modeLabel := map[config.Mode]string{
		config.ModeDryRun: "DRY-RUN",
		config.ModeSim:    "SIM",
		config.ModeLive:   "LIVE",
	}[cfg.Mode]

	switch cfg.Mode {
	case config.ModeDryRun:
		log.Info("=== DRY-RUN MODE — no real orders, P&L not tracked ===")
	case config.ModeSim:
		log.Info("=== SIM MODE — real feeds, simulated fills, P&L tracked ===")
	case config.ModeLive:
		log.Warn("=== LIVE MODE — REAL MONEY AT RISK ===")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exec, err := execution.NewExecutor(cfg.PrivateKey, cfg.Mode, cfg.EnableDumpHedgeLive, log)
	if err != nil {
		log.Fatal("executor init failed", zap.Error(err))
	}
	log.Info("wallet loaded", zap.String("address", exec.Address()))

	clobClient := market.NewClient(
		cfg.PolyAPIKey, cfg.PolyAPISecret, cfg.PolyAPIPassphrase,
		exec.Address(), log,
	)
	exec.SetCLOBClient(clobClient)

	if cfg.Mode == config.ModeLive && !*force {
		strategies := fmt.Sprintf("oracle_lag=%v window_delta=%v dump_hedge=%v (live=%v)",
			cfg.EnableOracleLag, cfg.EnableWindowDelta, cfg.EnableDumpHedge, cfg.EnableDumpHedgeLive)
		fmt.Printf("\n*** LIVE TRADING MODE — REAL MONEY AT RISK ***\n")
		fmt.Printf("Wallet:          %s\n", exec.Address())
		fmt.Printf("Max bet:         $%.0f\n", cfg.MaxBetUSDC)
		fmt.Printf("Max daily loss:  $%.0f\n", cfg.MaxDailyLossUSDC)
		fmt.Printf("Strategies:      %s\n", strategies)
		fmt.Printf("Assets:          %s\n", *assets)
		fmt.Printf("\nType 'yes' to confirm: ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "yes" {
			log.Fatal("live trading cancelled")
		}
	}

	// Build list of enabled assets
	enabledSymbols := make(map[string]bool)
	for _, sym := range strings.Split(*assets, ",") {
		enabledSymbols[strings.TrimSpace(strings.ToLower(sym))] = true
	}
	var activeAssets []asset.Asset
	for _, a := range asset.All {
		if enabledSymbols[a.Symbol] {
			activeAssets = append(activeAssets, a)
		}
	}
	if len(activeAssets) == 0 {
		log.Fatal("no valid assets specified")
	}
	log.Info("active assets", zap.Int("count", len(activeAssets)))

	state := &botState{}
	state.init(modeLabel, exec.Address())

	ensemble := strategy.NewEnsemble(cfg.EnableOracleLag, cfg.EnableWindowDelta, cfg.EnableDumpHedge)

	var wg sync.WaitGroup

	// Launch one set of goroutines per asset
	for _, a := range activeAssets {
		a := a // capture loop variable

		ticks := make(chan feed.Tick, 100)
		prices := make(chan feed.AggregatedPrice, 50)
		oraclePrices := make(chan oracle.Price, 10)
		marketStates := make(chan market.State, 10)

		var livePriceBits atomic.Uint64
		var oraclePriceBits atomic.Uint64

		// Risk manager (per-asset)
		rm := risk.NewManager(risk.Config{
			MaxBetUSDC:        cfg.MaxBetUSDC,
			MaxDailyLossUSDC:  cfg.MaxDailyLossUSDC,
			MaxConcurrentBets: cfg.MaxConcurrentBets,
			KellyFraction:     cfg.KellyFraction,
		}, log)

		// Sim setup (per-asset)
		var simulator *sim.Simulator
		if cfg.Mode == config.ModeSim {
			journalPath := fmt.Sprintf("sim_journal_%s_%s.jsonl", a.Symbol, time.Now().Format("20060102_150405"))
			simulator, err = sim.NewSimulator(&livePriceBits, &oraclePriceBits, journalPath, log)
			if err != nil {
				log.Fatal("sim init failed", zap.String("asset", a.Symbol), zap.Error(err))
			}
			log.Info("sim journal opened", zap.String("asset", a.Symbol), zap.String("path", journalPath))
		}

		// Live journal (per-asset)
		var liveJournal *monitor.Journal
		if cfg.Mode == config.ModeLive {
			journalPath := fmt.Sprintf("trade_journal_%s_%s.jsonl", a.Symbol, time.Now().Format("20060102_150405"))
			liveJournal, err = monitor.NewJournal(journalPath)
			if err != nil {
				log.Fatal("live journal init failed", zap.String("asset", a.Symbol), zap.Error(err))
			}
			log.Info("live journal opened", zap.String("asset", a.Symbol), zap.String("path", journalPath))
		}

		// Price feeds
		wg.Add(1)
		go func() { defer wg.Done(); feed.RunBinance(ctx, a.BinancePair, ticks, log) }()
		wg.Add(1)
		go func() { defer wg.Done(); feed.RunBitstamp(ctx, a.BitstampChannel, ticks, log) }()
		wg.Add(1)
		go func() { defer wg.Done(); feed.RunCoinbase(ctx, a.CoinbaseProduct, ticks, log) }()
		wg.Add(1)
		go func() { defer wg.Done(); feed.RunAggregator(ctx, ticks, prices, log) }()

		// Oracle poller
		wg.Add(1)
		go func() { defer wg.Done(); oracle.RunPoller(ctx, cfg.PolygonRPC, a.OracleAddr, oraclePrices, log) }()

		// Market watcher
		wg.Add(1)
		go func() { defer wg.Done(); market.RunWatcher(ctx, clobClient, a.MarketSlugPfx, marketStates, log) }()

		// Settlement loop
		wg.Add(1)
		go func(rm *risk.Manager, simulator *sim.Simulator) {
			defer wg.Done()
			runSettlementLoop(ctx, rm, simulator, log)
		}(rm, simulator)

		// Periodic stats
		wg.Add(1)
		go func(sym string, rm *risk.Manager, simulator *sim.Simulator) {
			defer wg.Done()
			t := time.NewTicker(5 * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if simulator != nil {
						monitor.LogSimStats(log, rm, simStratStats(simulator))
					} else {
						monitor.LogStats(log, rm)
					}
				}
			}
		}(a.Symbol, rm, simulator)

		// Asset event loop
		wg.Add(1)
		go func(rm *risk.Manager, simulator *sim.Simulator, liveJournal *monitor.Journal) {
			defer wg.Done()
			runAssetLoop(
				ctx, a, exec, rm, simulator, liveJournal, ensemble,
				&livePriceBits, &oraclePriceBits,
				prices, oraclePrices, marketStates,
				func(as ui.AssetState) { state.setAsset(as) },
				log,
			)
		}(rm, simulator, liveJournal)

		// Defer shutdown reporting per asset
		defer func(sym string, rm *risk.Manager, simulator *sim.Simulator, liveJournal *monitor.Journal) {
			if simulator != nil {
				monitor.LogSimStats(log, rm, simStratStats(simulator))
				if err := simulator.Close(); err != nil {
					log.Warn("journal close error", zap.String("asset", sym), zap.Error(err))
				}
				log.Info("journal saved", zap.String("asset", sym), zap.String("path", simulator.JournalPath()))
			} else {
				monitor.LogStats(log, rm)
			}
			if liveJournal != nil {
				if err := liveJournal.Close(); err != nil {
					log.Warn("live journal close error", zap.String("asset", sym), zap.Error(err))
				}
				log.Info("live journal saved", zap.String("asset", sym), zap.String("path", liveJournal.Path()))
			}
		}(a.Symbol, rm, simulator, liveJournal)
	}

	// Web dashboard
	webDashboard(webMode, webPort, state, log, ctx)
	
	// Wait for shutdown signal and graceful shutdown
	shutdownAndWait(cancel, &wg, log)
	log.Info("bot stopped")
	
	log.Sync()
}

func webDashboard(webMode *bool, webPort *int, state *botState, log *zap.Logger, ctx context.Context) {
	// Web dashboard + state pusher
	if *webMode {
		webHub := web.NewHub()
		go webHub.Run()
		srv := web.NewServer(*webPort, webHub, ".")
		go func() {
			if err := srv.Run(ctx); err != nil {
				log.Warn("web server stopped", zap.Error(err))
			}
		}()
		log.Info("web dashboard started", zap.Int("port", *webPort))
	
		// Push merged state to hub every 500ms
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					webHub.Push(state.snapshot())
				}
			}
		}()
	}
}

func shutdownAndWait(cancel context.CancelFunc, wg *sync.WaitGroup, log *zap.Logger) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info("shutting down...")
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		log.Warn("shutdown timeout")
	}
}