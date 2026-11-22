package companion

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// TestMinSDKMatchesTheApp: MinSDK is the app's minSdk, so Pinky offers the
// app to exactly the phones that can install it.
func TestMinSDKMatchesTheApp(t *testing.T) {
	b, err := os.ReadFile("../../../../android/companion/app/build.gradle.kts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*minSdk\s*=\s*(\d+)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no minSdk in build.gradle.kts")
	}
	if n, _ := strconv.Atoi(string(m[1])); n != MinSDK {
		t.Errorf("build.gradle.kts has minSdk = %d; MinSDK is %d", n, MinSDK)
	}
}
