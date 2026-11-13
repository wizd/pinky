package adb

import (
	"slices"
	"testing"
)

// TestServerPorts: the ports of the adb servers in ps's list, in the forms
// adb's start-server, Pinky and launchd run them; not those of adb client
// commands, other programs, socket files or other computers' addresses.
func TestServerPorts(t *testing.T) {
	out := `    1 /sbin/launchd
19604 adb -L tcp:5047 fork-server server --reply-fd 4
20946 /Users/me/Library/Android/sdk/platform-tools/adb -L tcp:5037 fork-server server --reply-fd 3
20947 /Users/me/My SDK/adb -L tcp:5037 fork-server server --reply-fd 3
  501 /opt/homebrew/bin/adb -L tcp:localhost:5049 fork-server server --reply-fd 4 --one-device x
  502 /usr/local/bin/adb -L tcp:5041 server nodaemon
  503 adb -P 5043 nodaemon server
  504 adb -P5045 nodaemon server
  505 adb nodaemon server
  506 adb -L tcp:192.168.1.5:5051 fork-server server --reply-fd 4
  507 adb -L localfilesystem:/tmp/adb.sock fork-server server --reply-fd 4
  508 adb -P 5053 devices -l
  509 adb -s emulator-5600 shell run-as app.pinky.companion.dev ./corectl stop
  510 grep -L tcp:5055 fork-server server
  511 dns-sd -B _adb._tcp local.
  512 /usr/bin/scrcpy -L tcp:5057 fork-server server
x /bin/zsh
`
	want := []int{5037, 5041, 5043, 5045, 5047, 5049}
	if got := serverPorts(out); !slices.Equal(got, want) {
		t.Fatalf("serverPorts = %v, want %v", got, want)
	}
}
