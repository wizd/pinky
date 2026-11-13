package pilot

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// price is what a model costs, in US dollars a million tokens. Cache
// writes are those of the 5-minute cache, the one Pinky uses; a service
// that charges nothing for them, or has none, leaves them zero.
type price struct {
	input, output, cacheRead, cacheWrite float64
	// Above longPrompt tokens a request costs long instead (Haiku 5.5).
	longPrompt int64
	long       *price
	// Outside the hours peak reports a request costs offPeak instead
	// (DeepSeek).
	peak    func(time.Time) bool
	offPeak *price
	// From changes on a request costs next instead: a price the service
	// announced.
	changes time.Time
	next    *price
}

// prices are the Claude models' prices on Anthropic's API, by the start
// of the model's name (a dated name costs what its model does). They
// count for every service that speaks the Messages API without a table
// of its own: Anthropic, and an address the person typed.
var prices = map[string]price{
	"claude-opus-5-5":   {input: 4, output: 20, cacheRead: 0.20, cacheWrite: 5},
	"claude-opus-5":     {input: 5, output: 25, cacheRead: 0.50, cacheWrite: 6.25},
	"claude-sonnet-5-5": {input: 2, output: 10, cacheRead: 0.10, cacheWrite: 2.50},
	"claude-sonnet-5":   {input: 2, output: 10, cacheRead: 0.20, cacheWrite: 2.50},
	"claude-fable-5-2":  {input: 8, output: 40, cacheRead: 0.20, cacheWrite: 10},
	"claude-haiku-5-5": {input: 0.10, output: 0.50, cacheRead: 0.01, cacheWrite: 0.125,
		longPrompt: 100_000, long: &price{input: 0.50, output: 2.50, cacheRead: 0.05, cacheWrite: 0.625}},
}

// servicePrices are other services' prices in US dollars, by service ID
// (Services), then by the start of the model's name. Only prices the
// service publishes in US dollars for the address Services gives, as of
// 2026-11-13 (docs/notes/model-services.md); a service without them
// counts tokens.
var servicePrices = map[string]map[string]price{
	OpenAI: {
		// Prompts over 272K tokens cost more; Pinky's stay far below.
		"gpt-6-astra":   {input: 10, output: 50, cacheRead: 1},
		"gpt-6.1-sol":   {input: 2, output: 10, cacheRead: 0.10},
		"gpt-6.1-terra": {input: 1, output: 5, cacheRead: 0.05},
		"gpt-6-luna":    {input: 0.10, output: 0.50, cacheRead: 0.01},
		"gpt-5.6-terra": {input: 2, output: 12, cacheRead: 0.20},
	},
	"deepseek": {
		"deepseek-flash": {input: 0.30, output: 1.20, cacheRead: 0.006,
			peak: deepseekPeak, offPeak: &price{input: 0.15, output: 0.60, cacheRead: 0.003}},
		"deepseek-v4-pro": {input: 1.32, output: 3.96, cacheRead: 0.044,
			peak: deepseekPeak, offPeak: &price{input: 0.66, output: 1.98, cacheRead: 0.022}},
	},
	"gemini": {
		"gemini-4-hydrogen": {input: 1.50, output: 7.50, cacheRead: 0.075},
		"gemini-4-carbon": {input: 2, output: 10, cacheRead: 0.10,
			changes: geminiRise, next: &price{input: 4, output: 20, cacheRead: 0.20}},
		"gemini-4-argon": {input: 2, output: 10, cacheRead: 0.10,
			changes: geminiRise, next: &price{input: 4, output: 20, cacheRead: 0.20}},
		"gemini-3.5-flash-lite": {input: 0.30, output: 2.50, cacheRead: 0.03},
	},
	// Model Studio gives input and output; a cache hit costs a tenth of
	// the input.
	"qwen-intl": {
		"qwen3.8-max":   {input: 2, output: 6, cacheRead: 0.20},
		"qwen3.7-plus":  {input: 0.4, output: 1.6, cacheRead: 0.04},
		"qwen3.8-flash": {input: 0.15, output: 0.47, cacheRead: 0.015},
	},
	"qwen-cn": {
		"qwen3.8-max":   {input: 1.65, output: 4.951, cacheRead: 0.165},
		"qwen3.7-plus":  {input: 0.276, output: 1.101, cacheRead: 0.0276},
		"qwen3.8-flash": {input: 0.113, output: 0.382, cacheRead: 0.0113},
	},
	"glm-intl": {
		"glm-5.3":        {input: 1.4, output: 4.4, cacheRead: 0.26},
		"glm-5.3-flash":  {input: 0.15, output: 0.50, cacheRead: 0.03},
		"glm-5.3-flashx": {input: 0.37, output: 1.25, cacheRead: 0.075},
	},
}

// geminiRise is when Gemini 4 Carbon and Argon cost twice as much.
var geminiRise = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

// deepseekPeak reports whether t is in DeepSeek's peak hours: 01:00 to
// 04:00 and 06:00 to 10:00 UTC, Monday to Friday. Chinese public holidays
// are off-peak too, which Pinky does not know: it counts them as peak.
func deepseekPeak(t time.Time) bool {
	t = t.UTC()
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := t.Hour()
	return h >= 1 && h < 4 || h >= 6 && h < 10
}

// priceTable is the price table of the service of cfg, if Pinky has one.
func priceTable(cfg Config) map[string]price {
	if t, ok := servicePrices[cfg.Service]; ok {
		return t
	}
	if cfg.Provider != Anthropic {
		return nil
	}
	if s, ok := lookup(cfg.Service); ok && !s.Custom && s.ID != Anthropic {
		// Another service that speaks the Messages API sets its own
		// prices.
		return nil
	}
	return prices
}

// priceOf returns the prices of the model of cfg, if Pinky knows them:
// for Claude models on the Messages API, and for the models of the
// services in servicePrices.
func priceOf(cfg Config) (price, bool) {
	t := priceTable(cfg)
	best := ""
	for name := range t {
		if strings.HasPrefix(cfg.Model, name) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return price{}, false
	}
	return t[best], true
}

// pricedModels are the starts of the model names whose prices Pinky knows
// on s (Service.PricedModels).
func pricedModels(s Service) []string {
	t := priceTable(Config{Service: s.ID, Provider: s.API})
	return slices.Sorted(maps.Keys(t))
}

// at is what a request costs at t.
func (p price) at(t time.Time) price {
	if p.next != nil && !t.Before(p.changes) {
		return p.next.at(t)
	}
	if p.offPeak != nil && p.peak != nil && !p.peak(t) {
		return *p.offPeak
	}
	return p
}

// cost is what a request cost, in US dollars.
func (p price) cost(u usage) float64 {
	if p.long != nil && u.input+u.cacheRead+u.cacheWrite > p.longPrompt {
		p = *p.long
	}
	return (float64(u.input)*p.input + float64(u.output)*p.output +
		float64(u.cacheRead)*p.cacheRead + float64(u.cacheWrite)*p.cacheWrite) / 1e6
}
