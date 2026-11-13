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

cleanup() {
	rm -rf "$dst"
	rmdir cmd/pinky/resources 2>/dev/null || true
}
trap cleanup EXIT

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
