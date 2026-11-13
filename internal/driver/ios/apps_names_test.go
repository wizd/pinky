package ios

import (
	"context"
	"slices"
	"testing"

	"github.com/danielpaulus/go-ios/ios/installationproxy"

	"github.com/DIYgod/pinky/internal/driver"
)

// The home screen's layout, as SpringBoard sends it (format 2): the dock
// first, then pages; folders hold pages of their own; widgets have no
// bundle.
func TestHomeScreenNames(t *testing.T) {
	layout := []any{
		[]any{ // the dock
			map[string]any{"bundleIdentifier": "com.apple.mobilephone", "displayName": "电话"},
		},
		[]any{ // a page
			map[string]any{"bundleIdentifier": "com.tencent.xin", "displayName": "微信"},
			map[string]any{"iconType": "custom", "elementType": "widget", "displayName": "Clock"},
			map[string]any{"displayName": "社交", "listType": "folder", "iconLists": []any{
				[]any{map[string]any{"bundleIdentifier": "com.tencent.weread", "displayName": "微信读书"}},
			}},
		},
	}
	names := map[string]string{}
	collectIconNames(layout, names, 0)
	want := map[string]string{"com.apple.mobilephone": "电话", "com.tencent.xin": "微信", "com.tencent.weread": "微信读书"}
	if len(names) != len(want) {
		t.Fatalf("names = %v", names)
	}
	for id, n := range want {
		if names[id] != n {
			t.Errorf("%s = %q, want %q", id, names[id], n)
		}
	}
}

// The app list names apps as the home screen does, in the phone's language,
// and keeps the bundle's own names as aliases (GitHub issue #7: WeChat is
// 微信 on a Chinese iPhone, while its bundle says WeChat in every language).
// An app only in the App Library keeps its bundle's name. The app in front,
// found by its bundle's path, and a URL scheme's apps are named the same.
func TestAppListUsesHomeScreenNames(t *testing.T) {
	infos := []installationproxy.AppInfo{
		{"CFBundleIdentifier": "com.tencent.xin", "CFBundleDisplayName": "WeChat", "CFBundleName": "WeChat",
			"ApplicationType": "User", "Path": "/private/var/containers/Bundle/Application/A/WeChat.app",
			"CFBundleURLTypes": []any{map[string]any{"CFBundleURLSchemes": []any{"weixin"}}}},
		{"CFBundleIdentifier": "com.tencent.weread", "CFBundleDisplayName": "微信读书", "ApplicationType": "User"},
		{"CFBundleIdentifier": "com.apple.mobilephone", "CFBundleName": "MobilePhone", "CFBundleDisplayName": "Phone", "ApplicationType": "System"},
		{"CFBundleIdentifier": "com.example.library", "CFBundleName": "Library Only", "ApplicationType": "User"},
	}
	names := map[string]string{"com.tencent.xin": "微信", "com.tencent.weread": "微信读书", "com.apple.mobilephone": "电话"}
	l := readAppInfos(infos, names)
	got := map[string]driver.App{}
	for _, a := range l.apps {
		got[a.ID] = a
	}
	check := func(id, name string, aliases ...string) {
		t.Helper()
		a := got[id]
		if a.Name != name || !slices.Equal(a.Aliases, aliases) {
			t.Errorf("%s = %q %q, want %q %q", id, a.Name, a.Aliases, name, aliases)
		}
	}
	check("com.tencent.xin", "微信", "WeChat")
	check("com.tencent.weread", "微信读书")
	check("com.apple.mobilephone", "电话", "Phone")
	check("com.example.library", "Library Only")

	c := &appCatalog{}
	c.set(l)
	if a, ok := c.appForExecutable("/private/var/containers/Bundle/Application/A/WeChat.app/WeChat"); !ok || a.Name != "微信" {
		t.Errorf("app in front = %+v, %v", a, ok)
	}
	if s := c.schemeApps("weixin"); len(s) != 1 || s[0].Name != "微信" {
		t.Errorf("weixin's apps = %+v", s)
	}
	// The frontmost app's element and banners name it either way.
	for _, n := range []string{"微信", "WeChat"} {
		if a, ok := c.byName(n); !ok || a.ID != "com.tencent.xin" {
			t.Errorf("byName(%q) = %+v, %v", n, a, ok)
		}
	}
}

// Through WebDriverAgent, the app in front takes the app list's name, the
// home screen's, once the list has been read; before, WDA's own.
func TestWDAAppInfoUsesListName(t *testing.T) {
	d := &WDADevice{}
	front := wdaApp{BundleID: "com.tencent.xin", Name: "WeChat"}
	if got := d.appInfo(context.Background(), front, nil); got.Name != "WeChat" {
		t.Errorf("before the list: %+v", got)
	}
	d.apps = &appCatalog{}
	d.apps.set(readAppInfos([]installationproxy.AppInfo{
		{"CFBundleIdentifier": "com.tencent.xin", "CFBundleDisplayName": "WeChat", "ApplicationType": "User"},
	}, map[string]string{"com.tencent.xin": "微信"}))
	if got := d.appInfo(context.Background(), front, nil); got.Name != "微信" || got.ID != "com.tencent.xin" {
		t.Errorf("with the list: %+v", got)
	}
}
