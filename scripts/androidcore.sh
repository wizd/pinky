#!/bin/sh
# Builds Pinky's Go core for the Android app (docs/notes/android-app.md):
# internal/standalone/libcore as libpinkycore.so for arm64 with the Android
# NDK, into android/companion/app/build/go/jniLibs, where the app's debug
# and release builds pick it up. Then builds the debug APK, which runs
# tasks on the phone itself.
#
#	scripts/androidcore.sh          # the core, then the debug APK
#	scripts/androidcore.sh core     # only the core
#
# The core is arm64 only. Android installs an APK with native code only on
# phones that run one of its ABIs, so the APK also carries a tiny empty
# library, libpinkystub.so, for armeabi-v7a (32-bit-only phones) and
# x86_64 (emulators): there Pinky works with a computer, and the core,
# which is not there, does not load.
#
# Needs the Android SDK with an NDK (Android Studio's SDK Manager) and a JDK.
set -eu
cd "$(dirname "$0")/.."

ANDROID_HOME=${ANDROID_HOME:-$HOME/Library/Android/sdk}
JAVA_HOME=${JAVA_HOME:-/Applications/Android Studio.app/Contents/jbr/Contents/Home}
export ANDROID_HOME JAVA_HOME
NDK=${ANDROID_NDK_HOME:-$(ls -d "$ANDROID_HOME"/ndk/* 2>/dev/null | sort -V | tail -1)}
[ -d "$NDK" ] || { echo "androidcore: no Android NDK under $ANDROID_HOME/ndk" >&2; exit 1; }
host=$(uname -s | tr '[:upper:]' '[:lower:]')-x86_64 # the NDK ships x86_64 hosts only; Apple silicon runs them
bin="$NDK/toolchains/llvm/prebuilt/$host/bin"

libs=android/companion/app/build/go/jniLibs
mkdir -p "$libs/arm64-v8a"
version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
# Android 15 and later warn about libraries not aligned to 16 KB pages.
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$bin/aarch64-linux-android29-clang" \
	go build -trimpath -buildmode=c-shared \
	-ldflags "-s -w -X main.version=$version -extldflags=-Wl,-z,max-page-size=16384" \
	-o "$libs/arm64-v8a/libpinkycore.so" ./internal/standalone/libcore
rm -f "$libs/arm64-v8a/libpinkycore.h"

# The stand-ins for the other ABIs: one empty function, no libc.
src=$(mktemp "${TMPDIR:-/tmp}/pinkystub.XXXXXX")
trap 'rm -f "$src"' EXIT
echo 'void pinky_no_core(void) {}' >"$src"
for t in armeabi-v7a:armv7a-linux-androideabi29 x86_64:x86_64-linux-android29; do
	abi=${t%%:*}
	mkdir -p "$libs/$abi"
	"$bin/${t#*:}-clang" -x c -shared -nostdlib -s -Wl,-z,max-page-size=16384 \
		-o "$libs/$abi/libpinkystub.so" "$src"
done
ls -l "$libs"/*/*.so

[ "${1:-}" = core ] && exit 0
android/companion/gradlew --quiet -p android/companion assembleDebug
ls -l android/companion/app/build/outputs/apk/debug/*.apk
