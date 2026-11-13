#!/bin/sh
# Builds Pinky's iPhone app (see README.md).
#
#   ios/build.sh core                    the Go core only (libpinkycore.a)
#   ios/build.sh full  [xcodebuild args] app + packet tunnel + Live Activity
#   ios/build.sh lite  [xcodebuild args] app + Live Activity, no tunnel (use
#                                        LocalDevVPN; for a personal team)
#   ios/build.sh sim   [xcodebuild args] the full build for the Simulator,
#                                        unsigned: for the app's screens (a
#                                        Simulator runs no packet tunnel)
#   ios/build.sh kit   <out.tar.gz> [version]
#                                        the iPhone app kit a release carries:
#                                        the Xcode project, the sources and
#                                        the core built for phones, from
#                                        which desktop Pinky builds the app
#                                        with the person's team
#                                        (internal/driver/ios/appinstall.go)
#
# Xcode builds unsigned; this script signs each part with the development
# profile of its App ID, or the team's wildcard profile when it has none, so
# no Xcode account is needed. The full build needs explicit App IDs with the
# Network Extension capability and their profiles (README.md). PINKY_SIGN=0
# skips signing (a compile check).
#
# PINKY_WDA=<WebDriverAgentRunner.xctest> puts WebDriverAgent's UI test
# bundle (from a build-for-testing of WebDriverAgent, inside
# WebDriverAgentRunner-Runner.app/PlugIns) into the app, which then hosts
# WebDriverAgent for Pinky on a computer in test mode (App/TestHost.swift),
# as desktop Pinky builds it.
#
# PINKY_BUNDLE_ID sets the app's bundle id (the extensions add .tunnel and
# .activity; lite adds .lite); it must be an App ID of PINKY_TEAM, which
# overrides the team. PINKY_PAIRING=<rppair pair's file> and
# PINKY_UDID=<udid> put the pairing into the app (build/Pairing.json, never
# committed), which moves it into the Keychain on its first start.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
app=$here/app
out=$app/build
team=${PINKY_TEAM:-492J8Q67PF}
what=${1:-full}
[ $# -gt 0 ] && shift

mkdir -p "$out/core"

if [ "$what" = sim ]; then
	sdk=$(xcrun --sdk iphonesimulator --show-sdk-path)
	cc="$(xcrun --sdk iphonesimulator -f clang) -isysroot $sdk -target arm64-apple-ios17.0-simulator"
else
	sdk=$(xcrun --sdk iphoneos --show-sdk-path)
	cc="$(xcrun --sdk iphoneos -f clang) -isysroot $sdk -arch arm64 -miphoneos-version-min=17.0"
fi
echo "== Go core"
(cd "$root" && GOOS=ios GOARCH=arm64 CGO_ENABLED=1 CC="$cc" \
	go build -buildmode=c-archive -trimpath -ldflags=-s -o "$out/core/libpinkycore.a" ./ios/core)
[ "$what" = core ] && exit 0

if [ "$what" = kit ]; then
	dest=${1:?usage: $0 kit <out.tar.gz> [version]}
	version=${2:-0.1}
	case $dest in /*) ;; *) dest=$(pwd)/$dest ;; esac
	stage=$(mktemp -d)
	trap 'rm -rf "$stage"' EXIT
	kit=$stage/pinky-iphone
	mkdir -p "$kit/build/core"
	cp -R "$app/App" "$app/Shared" "$app/Activity" "$app/Tunnel" "$app/project.yml" "$kit/"
	cp "$out/core/libpinkycore.a" "$out/core/libpinkycore.h" "$kit/build/core/"
	echo '{}' >"$kit/build/Pairing.json" # never a pairing of this Mac's
	# The release's version, so the phone shows which Pinky built it.
	sed -i '' "s/^    MARKETING_VERSION: .*/    MARKETING_VERSION: \"$version\"/" "$kit/project.yml"
	(cd "$kit" && xcodegen generate --quiet)
	COPYFILE_DISABLE=1 tar -C "$stage" -czf "$dest" pinky-iphone
	echo "kit $dest"
	exit 0
fi

if [ -n "${PINKY_PAIRING:-}" ]; then
	: "${PINKY_UDID:?PINKY_UDID is needed with PINKY_PAIRING}"
	umask 077
	printf '{"udid":"%s","pairing":"%s"}\n' "$PINKY_UDID" "$(base64 <"$PINKY_PAIRING" | tr -d '\n')" >"$out/Pairing.json"
elif [ ! -f "$out/Pairing.json" ]; then
	echo '{}' >"$out/Pairing.json" # a build without a pairing
fi

echo "== Xcode project"
(cd "$app" && xcodegen generate --quiet)

profiles="$HOME/Library/Developer/Xcode/UserData/Provisioning Profiles"

# profile <application-identifier>: the newest profile Xcode keeps for it
# that has not expired.
profile() {
	now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	best= bestDate=
	for p in "$profiles"/*.mobileprovision; do
		security cms -D -i "$p" >"$out/profile.plist" 2>/dev/null || continue
		[ "$(plutil -extract Entitlements.application-identifier raw -o - "$out/profile.plist")" = "$1" ] || continue
		[ "$(plutil -extract ExpirationDate raw -o - "$out/profile.plist")" \> "$now" ] || continue
		d=$(plutil -extract CreationDate raw -o - "$out/profile.plist")
		if [ -z "$best" ] || [ "$d" \> "$bestDate" ]; then
			best=$p bestDate=$d
		fi
	done
	rm -f "$out/profile.plist"
	[ -n "$best" ] && printf '%s\n' "$best"
}

# identity <profile>: one of the profile's certificates we hold, by SHA-1.
identity() {
	security cms -D -i "$1" >"$out/profile.plist"
	have=$(security find-identity -v -p codesigning | awk '{ print $2 }')
	i=0
	while cert=$(plutil -extract "DeveloperCertificates.$i" raw -o - "$out/profile.plist" 2>/dev/null); do
		h=$(printf '%s' "$cert" | base64 -d | shasum -a 1 | awk '{ print toupper($1) }')
		if printf '%s\n' "$have" | grep -qx "$h"; then
			rm -f "$out/profile.plist"
			printf '%s\n' "$h"
			return 0
		fi
		i=$((i + 1))
	done
	rm -f "$out/profile.plist"
	return 1
}

# sign <app>: signs WebDriverAgent's test bundle and its frameworks when
# the app holds them (PINKY_WDA) with the app's identity, as Xcode signs a
# test bundle, then the extensions, then the app, each with its profile and
# the entitlements project.yml gives its target (build/gen/<target>.entitlements,
# the target being the bundle's executable).
sign() {
	for test in "$1"/PlugIns/*.xctest; do
		[ -d "$test" ] || continue
		id=$(plutil -extract CFBundleIdentifier raw -o - "$1/Info.plist")
		p=$(profile "$team.$id") || p=$(profile "$team.*") || {
			echo "no development profile for $team.$id" >&2
			exit 1
		}
		who=$(identity "$p") || {
			echo "no signing identity for $p" >&2
			exit 1
		}
		for f in "$test"/Frameworks/*; do
			[ -e "$f" ] && codesign --force --sign "$who" --timestamp=none "$f"
		done
		codesign --force --sign "$who" --timestamp=none "$test"
		echo "signed $(basename "$test")"
	done
	for target in "$1"/PlugIns/*.appex "$1"; do
		id=$(plutil -extract CFBundleIdentifier raw -o - "$target/Info.plist")
		p=$(profile "$team.$id") || p=$(profile "$team.*") || {
			echo "no development profile for $team.$id" >&2
			exit 1
		}
		who=$(identity "$p") || {
			echo "no signing identity for $p" >&2
			exit 1
		}
		exe=$(plutil -extract CFBundleExecutable raw -o - "$target/Info.plist")
		ent=$out/sign.entitlements
		if [ -f "$out/gen/$exe.entitlements" ]; then
			cp "$out/gen/$exe.entitlements" "$ent"
		else
			plutil -create xml1 "$ent"
		fi
		plutil -replace application-identifier -string "$team.$id" "$ent"
		plutil -replace 'com\.apple\.developer\.team-identifier' -string "$team" "$ent"
		plutil -replace get-task-allow -bool YES "$ent"
		cp "$p" "$target/embedded.mobileprovision"
		codesign --force --sign "$who" --entitlements "$ent" --timestamp=none "$target"
		echo "signed $id with $(basename "$p")"
	done
	rm -f "$out/sign.entitlements"
}

# embed_wda <app> <WebDriverAgentRunner.xctest>: puts the test bundle into
# the app's PlugIns and marks the app as its host: the key desktop Pinky
# looks for (internal/driver/ios, wdaHostKey), and the background mode
# Xcode's runner has, which keeps the test running while other apps are in
# front.
embed_wda() {
	[ -d "$2" ] && [ "$(basename "$2")" = WebDriverAgentRunner.xctest ] || {
		echo "PINKY_WDA must be a WebDriverAgentRunner.xctest folder: $2" >&2
		exit 2
	}
	mkdir -p "$1/PlugIns"
	rm -rf "$1/PlugIns/WebDriverAgentRunner.xctest"
	cp -R "$2" "$1/PlugIns/"
	plutil -replace PinkyWebDriverAgentHost -bool YES "$1/Info.plist"
	modes=$(plutil -extract UIBackgroundModes json -o - "$1/Info.plist" 2>/dev/null || echo '[]')
	case $modes in
	*'"continuous"'*) ;;
	*) plutil -insert UIBackgroundModes -string continuous -append "$1/Info.plist" ;;
	esac
	echo "embedded WebDriverAgent's test"
}

platform=iphoneos destination=generic/platform=iOS
case $what in
full) scheme=Pinky ;;
lite) scheme=PinkyLite ;;
sim)
	scheme=Pinky platform=iphonesimulator destination="generic/platform=iOS Simulator" PINKY_SIGN=0
	set -- ARCHS=arm64 ONLY_ACTIVE_ARCH=NO "$@" # the Go core is arm64 only
	;;
*)
	echo "usage: $0 core|full|lite|sim [xcodebuild args], or $0 kit <out.tar.gz> [version]" >&2
	exit 2
	;;
esac

set -- DEVELOPMENT_TEAM="$team" "$@"
[ -n "${PINKY_BUNDLE_ID:-}" ] && set -- PINKY_BUNDLE_ID="$PINKY_BUNDLE_ID" "$@"
xcodebuild -project "$app/Pinky.xcodeproj" -scheme "$scheme" \
	-configuration Debug -destination "$destination" -derivedDataPath "$out/dd" \
	CODE_SIGNING_ALLOWED=NO "$@" build
bundle=$out/dd/Build/Products/Debug-$platform/$scheme.app
if [ -n "${PINKY_WDA:-}" ]; then
	embed_wda "$bundle" "$PINKY_WDA"
fi
[ "${PINKY_SIGN:-1}" = 1 ] && sign "$bundle"
echo "built $bundle"
