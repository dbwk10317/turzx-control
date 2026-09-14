#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Build the Windows ffmpeg.exe we bundle, from pinned upstream sources, with
# only the components internal/render drives. Runs anywhere with a working
# toolchain: MSYS2 UCRT64 natively, or WSL/Linux/macOS with a mingw-w64 cross
# compiler. The Windows-side verification lives in build-ffmpeg.ps1, which runs
# the real pipeline against the result.
set -euo pipefail

ffmpeg_ref=n9.0.1
x264_ref=stable
cross_prefix=
work=
out=

usage() {
    cat <<'USAGE'
usage: build-ffmpeg.sh --work <dir> --out <dir> [options]
  --work <dir>          scratch directory for the source trees
  --out <dir>           directory to write ffmpeg.exe and the build record into
  --ffmpeg-ref <ref>    FFmpeg tag, branch or commit (default n9.0.1)
  --x264-ref <ref>      x264 tag, branch or commit (default stable)
  --cross-prefix <p>    toolchain prefix for cross builds, e.g.
                        x86_64-w64-mingw32- on WSL, Linux or macOS.
                        Omit when building natively under MSYS2 UCRT64.
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --work) work=$2; shift 2 ;;
        --out) out=$2; shift 2 ;;
        --ffmpeg-ref) ffmpeg_ref=$2; shift 2 ;;
        --x264-ref) x264_ref=$2; shift 2 ;;
        --cross-prefix) cross_prefix=$2; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
done
[ -n "$work" ] && [ -n "$out" ] || { usage >&2; exit 2; }

for tool in git make nasm; do
    command -v "$tool" >/dev/null 2>&1 || { echo "missing build tool: $tool" >&2; exit 2; }
done
if [ -n "$cross_prefix" ]; then
    command -v "${cross_prefix}gcc" >/dev/null 2>&1 || { echo "missing cross compiler: ${cross_prefix}gcc" >&2; exit 2; }
fi

# The PNG decoder needs zlib for the target. Without this check the failure
# surfaces minutes later as an opaque FFmpeg configure error.
probe=$(mktemp -d)
printf '#include <zlib.h>
int main(void){return zlibVersion() == 0;}
' > "$probe/z.c"
if ! "${cross_prefix}gcc" "$probe/z.c" -lz -o "$probe/z" >/dev/null 2>&1; then
    rm -rf "$probe"
    echo "zlib for the target is missing; the PNG decoder needs it." >&2
    echo "  Rocky/RHEL:    sudo dnf install mingw64-zlib mingw64-zlib-static" >&2
    echo "  Debian/Ubuntu: sudo apt install mingw-w64" >&2
    echo "  MSYS2 UCRT64:  pacman -S mingw-w64-ucrt-x86_64-zlib" >&2
    exit 2
fi
rm -rf "$probe"

mkdir -p "$work" "$out"
work=$(cd "$work" && pwd)
out=$(cd "$out" && pwd)
deps="$work/deps"
mkdir -p "$deps"

# Resolve the ref to a commit first: a plain branch name would otherwise make
# git create a local branch, which --detach refuses.
fetch() {
    if [ ! -d "$1/.git" ]; then git clone "$2" "$1"; fi
    git -C "$1" fetch --tags --force origin
    local sha
    sha=$(git -C "$1" rev-parse --verify --quiet "$3^{commit}" || git -C "$1" rev-parse --verify "origin/$3^{commit}")
    git -C "$1" checkout --detach "$sha"
}

# Every enabled component maps to one step of the pipeline in
# internal/render/render.go; --disable-everything keeps the rest out. The
# verification step runs that exact pipeline, so a missing component fails the
# build instead of the product.
configure_flags="
--disable-everything
--disable-autodetect
--disable-doc
--disable-network
--disable-ffplay
--disable-ffprobe
--disable-shared
--enable-static
--enable-gpl
--enable-version3
--enable-zlib
--enable-libx264
--enable-demuxer=mov
--enable-decoder=h264
--enable-parser=h264
--enable-bsf=extract_extradata
--enable-demuxer=image2pipe
--enable-decoder=png
--enable-parser=png
--enable-filter=scale,crop,setsar,setpts,overlay,fps,transpose,format,null,buffer,buffersink
--enable-encoder=libx264
--enable-muxer=h264
--enable-protocol=file,pipe
"
configure_flags=$(echo $configure_flags)

jobs=$( (command -v nproc >/dev/null && nproc) || sysctl -n hw.ncpu 2>/dev/null || echo 4 )

cd "$work"
fetch x264 https://code.videolan.org/videolan/x264.git "$x264_ref"
(
    cd x264
    x264_flags="--prefix=$deps --enable-static --enable-pic --disable-cli --disable-opencl --host=x86_64-w64-mingw32"
    if [ -n "$cross_prefix" ]; then x264_flags="$x264_flags --cross-prefix=$cross_prefix"; fi
    ./configure $x264_flags
    make -j"$jobs"
    make install
)

fetch ffmpeg https://git.ffmpeg.org/ffmpeg.git "$ffmpeg_ref"
(
    cd ffmpeg
    export PKG_CONFIG_PATH="$deps/lib/pkgconfig"
    ffmpeg_flags="$configure_flags --pkg-config-flags=--static --extra-cflags=-I$deps/include --extra-ldflags=-L$deps/lib"
    if [ -n "$cross_prefix" ]; then
        ffmpeg_flags="$ffmpeg_flags --enable-cross-compile --arch=x86_64 --target-os=mingw32 --cross-prefix=$cross_prefix"
    fi
    ./configure $ffmpeg_flags
    make -j"$jobs"
)

cp ffmpeg/ffmpeg.exe "$out/ffmpeg.exe"
# License texts from the exact sources this build used, so the package carries
# the notices for what it actually ships.
cp ffmpeg/COPYING.GPLv3 "$out/ffmpeg-COPYING.GPLv3.txt"
cp ffmpeg/LICENSE.md "$out/ffmpeg-LICENSE.md"
cp x264/COPYING "$out/x264-COPYING.txt"
git -C x264 rev-parse HEAD > "$out/x264.commit"
git -C ffmpeg rev-parse HEAD > "$out/ffmpeg.commit"
printf '%s\n' "$configure_flags" > "$out/configure.txt"
printf 'ffmpeg_ref=%s\nx264_ref=%s\ncross_prefix=%s\n' "$ffmpeg_ref" "$x264_ref" "${cross_prefix:-none}" > "$out/build.env"

echo "built: $out/ffmpeg.exe"
echo "verify it on Windows: scripts\\build-ffmpeg.ps1 -FromBuildOutput <this directory>"
