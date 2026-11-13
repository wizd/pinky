//go:build ios

package standalone

import "github.com/DIYgod/pinky/internal/gateway"

// On the iPhone the screens get the gateway's English only (shown.go says
// why): no catalogs are linked into the network extension until its app
// names a language and the memory they take is measured.

func setLocale(string) {}

func lineShown(gateway.DeviceView) string { return "" }

func needShown(gateway.NeedView) (title, detail, rule string) { return "", "", "" }

func stepShown(string) (step, asked string) { return "", "" }
