package traffic

import (
	"net"
	"net/url"
	"slices"
	"strings"
)

// BlockedHosts are the domains of banking, payment, brokerage and
// password-manager services that Pinky's proxy never decrypts or records
// beyond their host, for every phone. A host matches a domain when it is
// the domain or ends in "." and the domain. The person adds more per phone
// (Gateway.SetTrafficBlocklist). docs/notes/traffic-capture.md has why.
var BlockedHosts = []string{
	// Payment, China.
	"alipay.com", "alipay.cn", "alipayobjects.com", "antgroup.com", "mybank.cn",
	"tenpay.com", "wechatpay.cn", "pay.weixin.qq.com", "payapp.weixin.qq.com", "api.mch.weixin.qq.com",
	"unionpay.com", "95516.com", "jdpay.com", "99bill.com", "lakala.com", "yeepay.com",
	// Banks, China.
	"icbc.com.cn", "ccb.com", "boc.cn", "bankofchina.com", "abchina.com", "bankcomm.com", "psbc.com",
	"cmbchina.com", "cmbimg.com", "spdb.com.cn", "cib.com.cn", "citicbank.com", "cebbank.com", "cmbc.com.cn",
	"pingan.com", "pingan.com.cn", "hxb.com.cn", "cgbchina.com.cn", "bankofbeijing.com.cn", "bosc.cn",
	"webank.com", "hsbc.com.cn", "standardchartered.com.cn",
	// Brokerage, China and Hong Kong.
	"htsc.com.cn", "gtja.com", "citics.com", "cmschina.com", "gf.com.cn", "95579.com",
	"futunn.com", "futuhk.com", "moomoo.com", "itiger.com", "tigerbrokers.com", "longbridge.com", "longbridgeapp.com",
	// Payment, elsewhere.
	"paypal.com", "paypalobjects.com", "paypal-activation.com", "braintreegateway.com", "braintree-api.com",
	"stripe.com", "stripe.network", "venmo.com", "wise.com", "transferwise.com", "revolut.com",
	"klarna.com", "afterpay.com", "squareup.com", "cash.app", "adyen.com", "checkout.com",
	"apple-pay-gateway.apple.com", "pay.google.com", "payments.google.com",
	// Banks and cards, elsewhere.
	"chase.com", "bankofamerica.com", "wellsfargo.com", "citi.com", "citibank.com", "capitalone.com",
	"usbank.com", "americanexpress.com", "discover.com", "hsbc.com", "barclays.co.uk", "lloydsbank.com",
	"monzo.com", "n26.com", "ing.com", "bnpparibas.com", "santander.com", "mufg.jp", "smbc.co.jp", "mizuhobank.co.jp",
	"rakuten-bank.co.jp", "paypay.ne.jp", "visa.com", "mastercard.com",
	// Brokerage and crypto, elsewhere.
	"schwab.com", "fidelity.com", "vanguard.com", "etrade.com", "robinhood.com", "interactivebrokers.com",
	"webull.com", "sbisec.co.jp", "rakuten-sec.co.jp", "coinbase.com", "binance.com", "kraken.com", "okx.com",
	// Password managers.
	"1password.com", "1password.eu", "1password.ca", "1passwordservices.com", "agilebits.com",
	"bitwarden.com", "bitwarden.eu", "bitwarden.net", "lastpass.com", "dashlane.com", "keepersecurity.com",
	"keepersecurity.eu", "nordpass.com", "pass.proton.me", "enpass.io", "roboform.com",
}

// HostMatches reports whether host is domain or one of its subdomains.
func HostMatches(host, domain string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	if host == "" || domain == "" {
		return false
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// Blocked reports whether host is on BlockedHosts or extra.
func Blocked(host string, extra []string) bool {
	for _, d := range BlockedHosts {
		if HostMatches(host, d) {
			return true
		}
	}
	for _, d := range extra {
		if HostMatches(host, d) {
			return true
		}
	}
	return false
}

// CleanHost turns what the person typed ("https://Bank.example/login",
// "*.bank.example", ".bank.example") into a domain to block, or "" when it
// is none.
func CleanHost(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Host
		}
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	s = strings.Trim(strings.TrimPrefix(s, "*."), ".[]")
	if s == "" || strings.ContainsAny(s, " \t*@") {
		return ""
	}
	return s
}

// CleanHosts cleans a list, dropping duplicates and what is no host.
func CleanHosts(in []string) []string {
	var out []string
	for _, s := range in {
		if h := CleanHost(s); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}
