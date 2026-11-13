#!/bin/sh
# Build a local Windows Pinky.exe for development (make windows-dev).
#
# The mirror needs FFmpeg 9's shared libraries next to the executable. When
# build/ffmpeg-windows/bin exists (scripts/ffmpeg.sh build/ffmpeg-windows
# windows-amd64 makes it; see README.md's Windows section), this stages its
# DLLs through MyGo's resources so every build carries them, and removes
# them afterwards, as scripts/dist.sh does for a release. Without it, the
# build still works: set PINKY_FFMPEG_DIR to a folder of FFmpeg 9 libraries.
set -eu
cd "$(dirname "$0")/.."

top=build/ffmpeg-windows
dst=cmd/pinky/resources/windows-amd64
cfg=cmd/pinky/mygo.json
cfgsave=$(mktemp)
cp "$cfg" "$cfgsave"

cleanup() {
	rm -rf "$dst"
	rmdir cmd/pinky/resources 2>/dev/null || true
	cp "$cfgsave" "$cfg"
	rm -f "$cfgsave"
}
trap cleanup EXIT

# The build's version: the last release it contains, the branch, how many
# commits past that release and the commit, such as
# 2026.1113.17-vcorp-dev.7.9b6fe46 (with .dirty for uncommitted changes).
# It is not only numbers, so the build never updates itself to a release,
# and it says dev, so it never fetches a release's iPhone kit either.
# Telemetry skips only 0.0.0-dev, so the build also empties its endpoint:
# a developer's own runs are not usage.
desc=$(git describe --tags --match 'v[0-9]*' --long --dirty 2>/dev/null || true)
if [ -n "$desc" ]; then
	branch=$(git rev-parse --abbrev-ref HEAD | tr -c 'A-Za-z0-9\n' '-')
	base=${desc#v}
	base=${base%%-*}
	rest=${desc#v"$base"-}
	count=${rest%%-*}
	sha=$(git rev-parse --short HEAD)
	version="$base-$branch-dev.$count.$sha"
	case $desc in *-dirty) version="$version.dirty" ;; esac
else
	version=0.0.0-dev
fi
sed "s/^\( *\"version\": *\"\)[^\"]*\"/\1$version\"/" "$cfgsave" >"$cfg"
echo "Pinky $version"
export MYGO_LDFLAGS="-X main.version=$version -X github.com/DIYgod/pinky/internal/telemetry.Endpoint="

if [ -d "$top/bin" ]; then
	mkdir -p "$dst"
	cp "$top"/bin/*.dll "$dst/"
	if [ -f "$top/COPYING.LGPLv2.1" ]; then
		cp "$top/COPYING.LGPLv2.1" "$dst/FFmpeg-COPYING.LGPLv2.1"
	fi
	if [ -f "$top/BUILD.txt" ]; then
		cp "$top/BUILD.txt" "$dst/FFmpeg-BUILD.txt"
	fi
	echo "carrying FFmpeg 9 from $top/bin"
else
	echo "no $top/bin: run scripts/ffmpeg.sh $top windows-amd64, or set PINKY_FFMPEG_DIR"
fi

go tool mygo build -platform windows/amd64 cmd/pinky
