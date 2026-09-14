# SPDX-License-Identifier: GPL-3.0-or-later
# Build the FFmpeg we bundle, from pinned upstream sources, with only the
# components internal/render actually drives. Building it ourselves keeps the
# GPL corresponding source trivially identifiable: this script plus the two
# recorded commits are the complete build recipe.
[CmdletBinding()]
param(
    # MSYS2 UCRT64 installation; see the local development environment section
    # of AGENTS.md for this machine's path.
    [Parameter(Mandatory = $true)][string]$Msys2Root,
    [string]$FFmpegRef = 'n9.0.1',
    [string]$X264Ref = 'stable',
    [string]$WorkDirectory = 'bin/ffmpeg-build',
    [string]$OutputDirectory = 'bin',
    # Any .mp4 drives the smoke test's background input.
    [string]$SmokeBackground = 'assets/backgrounds/azure-ribbon.mp4',
    [switch]$Clean
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot

function Join-ProjectPath([string]$Path) {
    if ([IO.Path]::IsPathRooted($Path)) { return $Path }
    return Join-Path $projectRoot $Path
}

$required = @{
    'usr\bin\bash.exe'       = 'base'
    'usr\bin\make.exe'       = 'make'
    'usr\bin\git.exe'        = 'git'
    'usr\bin\diff.exe'       = 'diffutils'
    'usr\bin\cygpath.exe'    = 'base'
    'ucrt64\bin\gcc.exe'     = 'mingw-w64-ucrt-x86_64-gcc'
    'ucrt64\bin\nasm.exe'    = 'mingw-w64-ucrt-x86_64-nasm'
    'ucrt64\bin\pkg-config.exe' = 'mingw-w64-ucrt-x86_64-pkgconf'
}
$missing = @()
foreach ($entry in $required.GetEnumerator()) {
    if (-not (Test-Path -LiteralPath (Join-Path $Msys2Root $entry.Key) -PathType Leaf)) { $missing += $entry.Value }
}
if ($missing) {
    $packages = ($missing | Sort-Object -Unique) -join ' '
    throw "MSYS2 is missing build tools. Install them first: $Msys2Root\usr\bin\pacman.exe -S --needed $packages"
}

$workDirectory = Join-ProjectPath $WorkDirectory
if ($Clean -and (Test-Path -LiteralPath $workDirectory)) { Remove-Item -LiteralPath $workDirectory -Recurse -Force }
if (-not (Test-Path -LiteralPath $workDirectory)) { New-Item -ItemType Directory -Path $workDirectory | Out-Null }
$outputDirectory = Join-ProjectPath $OutputDirectory
if (-not (Test-Path -LiteralPath $outputDirectory)) { New-Item -ItemType Directory -Path $outputDirectory | Out-Null }

$bash = Join-Path $Msys2Root 'usr\bin\bash.exe'
$cygpath = Join-Path $Msys2Root 'usr\bin\cygpath.exe'
$workUnix = (& $cygpath -u $workDirectory).Trim()

# Every enabled component maps to one step of the pipeline in
# internal/render/render.go; --disable-everything keeps the rest out. The smoke
# test below runs that exact pipeline, so a missing component fails the build
# instead of the product.
$configure = @(
    '--disable-everything'
    '--disable-autodetect'
    '--disable-doc'
    '--disable-network'
    '--disable-ffplay'
    '--disable-ffprobe'
    '--disable-shared'
    '--enable-static'
    '--enable-gpl'
    '--enable-version3'
    '--enable-zlib'
    '--enable-libx264'
    # Theme background: MP4 container with an H264 video stream.
    '--enable-demuxer=mov'
    '--enable-decoder=h264'
    '--enable-parser=h264'
    '--enable-bsf=extract_extradata'
    # Overlay frames: PNG images arriving on stdin.
    '--enable-demuxer=image2pipe'
    '--enable-decoder=png'
    '--enable-parser=png'
    # filter_complex: scale, crop, setsar, setpts, overlay, fps, transpose, format.
    '--enable-filter=scale,crop,setsar,setpts,overlay,fps,transpose,format,null,buffer,buffersink'
    # Output: H264 Annex B on stdout.
    '--enable-encoder=libx264'
    '--enable-muxer=h264'
    '--enable-protocol=file,pipe'
) -join ' '

$script = @"
set -euo pipefail
export MSYSTEM=UCRT64
export PATH=/ucrt64/bin:/usr/bin:`$PATH
cd "$workUnix"
deps="$workUnix/deps"
mkdir -p "`$deps"

fetch() {
    if [ ! -d "`$1/.git" ]; then git clone "`$2" "`$1"; fi
    git -C "`$1" fetch --tags --force origin
    git -C "`$1" checkout --detach "`$3"
}

fetch x264 https://code.videolan.org/videolan/x264.git "$X264Ref"
cd x264
./configure --prefix="`$deps" --host=x86_64-w64-mingw32 --enable-static --enable-pic --disable-cli --disable-opencl
make -j"`$(nproc)"
make install
cd ..

fetch ffmpeg https://git.ffmpeg.org/ffmpeg.git "$FFmpegRef"
cd ffmpeg
export PKG_CONFIG_PATH="`$deps/lib/pkgconfig"
./configure $configure --pkg-config-flags=--static --extra-cflags="-I`$deps/include" --extra-ldflags="-L`$deps/lib"
make -j"`$(nproc)"
cd ..

cp ffmpeg/ffmpeg.exe "$workUnix/ffmpeg.exe"
git -C x264 rev-parse HEAD > "$workUnix/x264.commit"
git -C ffmpeg rev-parse HEAD > "$workUnix/ffmpeg.commit"
"@

$scriptPath = Join-Path $workDirectory 'build.sh'
# LF only: the MSYS2 shell rejects CRLF line endings.
[IO.File]::WriteAllText($scriptPath, ($script -replace "`r`n", "`n"), (New-Object Text.UTF8Encoding($false)))
& $bash -lc ("bash '" + (& $cygpath -u $scriptPath).Trim() + "'")
if ($LASTEXITCODE -ne 0) { throw "FFmpeg build failed ($LASTEXITCODE)" }

$built = Join-Path $workDirectory 'ffmpeg.exe'
if (-not (Test-Path -LiteralPath $built -PathType Leaf)) { throw 'Build produced no ffmpeg.exe' }

# Smoke test: the real pipeline, not just --version. A PNG frame goes in on
# stdin over an MP4 background and Annex B H264 must come back on stdout.
$background = Join-ProjectPath $SmokeBackground
if (-not (Test-Path -LiteralPath $background -PathType Leaf)) { throw "Smoke test background not found: $background" }
Add-Type -AssemblyName System.Drawing
$framePath = Join-Path $workDirectory 'smoke-frame.png'
$bitmap = New-Object Drawing.Bitmap 1920, 462
try {
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    try { $graphics.Clear([Drawing.Color]::FromArgb(128, 32, 64, 96)) } finally { $graphics.Dispose() }
    $bitmap.Save($framePath, [Drawing.Imaging.ImageFormat]::Png)
} finally { $bitmap.Dispose() }

$filter = '[0:v]scale=1920:462:force_original_aspect_ratio=increase,crop=1920:462,setsar=1,setpts=PTS-STARTPTS[bg];' +
    '[1:v]setpts=PTS-STARTPTS[ov];[bg][ov]overlay=0:0:shortest=1:eof_action=endall,fps=25,transpose=clock,format=yuv420p[out]'
$smokeOutput = Join-Path $workDirectory 'smoke.h264'
$smokeError = Join-Path $workDirectory 'smoke.log'
$arguments = @(
    '-hide_banner', '-loglevel', 'warning',
    '-stream_loop', '-1', '-i', $background,
    '-f', 'image2pipe', '-framerate', '25', '-probesize', '32', '-analyzeduration', '0', '-threads', '1', '-c:v', 'png', '-i', 'pipe:0',
    '-filter_complex', $filter, '-map', '[out]', '-an',
    '-c:v', 'libx264', '-preset', 'veryfast', '-tune', 'zerolatency',
    '-pix_fmt', 'yuv420p', '-bf', '0', '-g', '25', '-keyint_min', '25', '-sc_threshold', '0',
    '-b:v', '1500k', '-minrate', '1500k', '-maxrate', '1500k', '-bufsize', '1500k',
    '-x264-params', 'nal-hrd=cbr:force-cfr=1', '-f', 'h264', 'pipe:1'
)
$smoke = Start-Process -FilePath $built -ArgumentList $arguments -NoNewWindow -PassThru -Wait `
    -RedirectStandardInput $framePath -RedirectStandardOutput $smokeOutput -RedirectStandardError $smokeError
if ($smoke.ExitCode -ne 0) { throw "Smoke test failed ($($smoke.ExitCode)): $(Get-Content -LiteralPath $smokeError -Raw)" }
$encoded = [IO.File]::ReadAllBytes($smokeOutput)
if ($encoded.Length -lt 4 -or $encoded[0] -ne 0 -or $encoded[1] -ne 0 -or $encoded[2] -ne 0 -or $encoded[3] -ne 1) {
    throw 'Smoke test produced no Annex B start code; the encoder or muxer selection is wrong.'
}

$target = Join-Path $outputDirectory 'ffmpeg.exe'
Copy-Item -LiteralPath $built -Destination $target -Force
$manifest = [ordered]@{
    built_at = (Get-Date).ToUniversalTime().ToString('o')
    ffmpeg = [ordered]@{ ref = $FFmpegRef; commit = (Get-Content -LiteralPath (Join-Path $workDirectory 'ffmpeg.commit') -Raw).Trim(); source = 'https://git.ffmpeg.org/ffmpeg.git' }
    x264 = [ordered]@{ ref = $X264Ref; commit = (Get-Content -LiteralPath (Join-Path $workDirectory 'x264.commit') -Raw).Trim(); source = 'https://code.videolan.org/videolan/x264.git' }
    configure = $configure
    sha256 = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
    build_script = 'scripts/build-ffmpeg.ps1'
}
$manifestPath = Join-Path $outputDirectory 'ffmpeg-build.json'
Set-Content -LiteralPath $manifestPath -Value ($manifest | ConvertTo-Json -Depth 4) -Encoding UTF8

[pscustomobject]@{
    Path = $target
    SizeMB = [math]::Round((Get-Item -LiteralPath $target).Length / 1MB, 1)
    Sha256 = $manifest.sha256
    Manifest = $manifestPath
} | Format-List
