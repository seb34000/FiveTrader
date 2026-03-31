package asset

// Asset holds the static configuration for one tradeable crypto asset.
type Asset struct {
	Symbol          string // lower-case ticker: "btc", "eth", …
	Name            string // display name: "Bitcoin"
	BinancePair     string // aggTrade stream pair: "btcusdt"
	BitstampChannel string // live_trades channel, or "" if not available on Bitstamp
	CoinbaseProduct string // Coinbase Advanced Trade product ID: "BTC-USD"
	// Chainlink aggregator address on Polygon mainnet.
	// Source: https://docs.chain.link/data-feeds/price-feeds/addresses?network=polygon
	// IMPORTANT: verify each address against the Chainlink docs before going live.
	OracleAddr    string
	MarketSlugPfx string // Polymarket slug prefix: "btc-updown-5m"
}

var (
	BTC = Asset{
		Symbol:          "btc",
		Name:            "Bitcoin",
		BinancePair:     "btcusdt",
		BitstampChannel: "live_trades_btcusd",
		CoinbaseProduct: "BTC-USD",
		OracleAddr:      "0xc907E116054Ad103354f2D350FD2514433D57F6F",
		MarketSlugPfx:   "btc-updown-5m",
	}
	ETH = Asset{
		Symbol:          "eth",
		Name:            "Ethereum",
		BinancePair:     "ethusdt",
		BitstampChannel: "live_trades_ethusd",
		CoinbaseProduct: "ETH-USD",
		OracleAddr:      "0xF9680D99D6C9589e2a93a78A04A279e509205945",
		MarketSlugPfx:   "eth-updown-5m",
	}
	SOL = Asset{
		Symbol:          "sol",
		Name:            "Solana",
		BinancePair:     "solusdt",
		BitstampChannel: "", // not listed on Bitstamp
		CoinbaseProduct: "SOL-USD",
		OracleAddr:      "0x10C8264C0935b3B9870013e057f330Ff3e9C56dC",
		MarketSlugPfx:   "sol-updown-5m",
	}
	XRP = Asset{
		Symbol:          "xrp",
		Name:            "XRP",
		BinancePair:     "xrpusdt",
		BitstampChannel: "live_trades_xrpusd",
		CoinbaseProduct: "XRP-USD",
		OracleAddr:      "0x785ba89291f676b5386652eB12b30cF361020694",
		MarketSlugPfx:   "xrp-updown-5m",
	}
	DOGE = Asset{
		Symbol:          "doge",
		Name:            "Dogecoin",
		BinancePair:     "dogeusdt",
		BitstampChannel: "", // not listed on Bitstamp
		CoinbaseProduct: "DOGE-USD",
		OracleAddr:      "0xbaf9327b6564454F4a3364C33eFeEf032b4b4444",
		MarketSlugPfx:   "doge-updown-5m",
	}

	// All is the canonical ordered list of supported assets.
	All = []Asset{BTC, ETH, XRP}
)
