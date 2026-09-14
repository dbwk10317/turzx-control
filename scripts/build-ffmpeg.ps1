# SPDX-License-Identifier: GPL-3.0-or-later
# Verify and record the FFmpeg we bundle. The build itself lives in
# build-ffmpeg.sh so it can run under MSYS2, WSL, Linux or macOS; this script
# either drives that build through MSYS2 or takes a build output produced
# elsewhere, then proves the binary on Windows against the real pipeline.
#
# Building it ourselves keeps the GPL corresponding source trivially
# identifiable: build-ffmpeg.sh plus the two recorded commits are the complete
# build recipe.
[CmdletBinding(DefaultParameterSetName = 'Build')]
param(
    # MSYS2 UCRT64 installation; see the local development environment section
    # of AGENTS.md for this machine's path.
    [Parameter(Mandatory = $true, ParameterSetName = 'Build')][string]$Msys2Root,
    [Parameter(ParameterSetName = 'Build')][string]$FFmpegRef = 'n9.0.1',
    [Parameter(ParameterSetName = 'Build')][string]$X264Ref = 'stable',
    [Parameter(ParameterSetName = 'Build')][string]$WorkDirectory = 'bin/ffmpeg-build',
    # A directory build-ffmpeg.sh produced elsewhere: ffmpeg.exe, the two
    # .commit files, configure.txt and build.env. Use this when the build ran
    # in WSL or on another machine.
    [Parameter(Mandatory = $true, ParameterSetName = 'Verify')][string]$FromBuildOutput,
    [string]$OutputDirectory = 'bin',
    # Any .mp4 drives the verification's background input.
    [string]$SmokeBackground = 'assets/backgrounds/azure-ribbon.mp4'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot

function Join-ProjectPath([string]$Path) {
    if ([IO.Path]::IsPathRooted($Path)) { return $Path }
    return Join-Path $projectRoot $Path
}

if ($PSCmdlet.ParameterSetName -eq 'Build') {
    $required = @{
        'usr\bin\bash.exe'          = 'base'
        'usr\bin\make.exe'          = 'make'
        'usr\bin\git.exe'           = 'git'
        'usr\bin\diff.exe'          = 'diffutils'
        'usr\bin\cygpath.exe'       = 'base'
        'ucrt64\bin\gcc.exe'        = 'mingw-w64-ucrt-x86_64-gcc'
        'ucrt64\bin\nasm.exe'       = 'mingw-w64-ucrt-x86_64-nasm'
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
    if (-not (Test-Path -LiteralPath $workDirectory)) { New-Item -ItemType Directory -Path $workDirectory | Out-Null }
    $buildOutput = Join-Path $workDirectory 'output'
    $bash = Join-Path $Msys2Root 'usr\bin\bash.exe'
    $cygpath = Join-Path $Msys2Root 'usr\bin\cygpath.exe'
    $scriptUnix = (& $cygpath -u (Join-Path $PSScriptRoot 'build-ffmpeg.sh')).Trim()
    $workUnix = (& $cygpath -u $workDirectory).Trim()
    $outputUnix = (& $cygpath -u $buildOutput).Trim()
    $command = "MSYSTEM=UCRT64 PATH=/ucrt64/bin:/usr/bin:`$PATH bash '$scriptUnix' --work '$workUnix' --out '$outputUnix' --ffmpeg-ref '$FFmpegRef' --x264-ref '$X264Ref'"
    & $bash -lc $command
    if ($LASTEXITCODE -ne 0) { throw "FFmpeg build failed ($LASTEXITCODE)" }
}
else {
    $buildOutput = Join-ProjectPath $FromBuildOutput
}

foreach ($name in @('ffmpeg.exe', 'ffmpeg.commit', 'x264.commit', 'configure.txt')) {
    if (-not (Test-Path -LiteralPath (Join-Path $buildOutput $name) -PathType Leaf)) {
        throw "Build output is missing ${name}: $buildOutput"
    }
}
$built = Join-Path $buildOutput 'ffmpeg.exe'

# Verification: the real pipeline, not just --version. PNG frames go in on
# stdin over an MP4 background and Annex B H264 must come back on stdout.
$background = Join-ProjectPath $SmokeBackground
if (-not (Test-Path -LiteralPath $background -PathType Leaf)) { throw "Verification background not found: $background" }
Add-Type -AssemblyName System.Drawing
$framePath = Join-Path $buildOutput 'smoke-frames.png'
$bitmap = New-Object Drawing.Bitmap 1920, 462
try {
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    try { $graphics.Clear([Drawing.Color]::FromArgb(128, 32, 64, 96)) } finally { $graphics.Dispose() }
    $buffer = New-Object IO.MemoryStream
    try { $bitmap.Save($buffer, [Drawing.Imaging.ImageFormat]::Png); $frame = $buffer.ToArray() } finally { $buffer.Dispose() }
} finally { $bitmap.Dispose() }
# image2pipe reads concatenated PNGs. The overlay ends the graph on the shorter
# input, so a single frame would end it before the looping background pairs with
# one; the product feeds a continuous stream, and so does this.
$file = [IO.File]::Create($framePath)
try { for ($i = 0; $i -lt 30; $i++) { $file.Write($frame, 0, $frame.Length) } } finally { $file.Dispose() }

$filter = '[0:v]scale=1920:462:force_original_aspect_ratio=increase,crop=1920:462,setsar=1,setpts=PTS-STARTPTS[bg];' +
    '[1:v]setpts=PTS-STARTPTS[ov];[bg][ov]overlay=0:0:shortest=1:eof_action=endall,fps=25,transpose=clock,format=yuv420p[out]'
$smokeOutput = Join-Path $buildOutput 'smoke.h264'
$smokeError = Join-Path $buildOutput 'smoke.log'
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
if ($smoke.ExitCode -ne 0) { throw "Verification failed ($($smoke.ExitCode)): $(Get-Content -LiteralPath $smokeError -Raw)" }
$encoded = [IO.File]::ReadAllBytes($smokeOutput)
# Annex B start codes are three or four bytes.
$annexB = $encoded.Length -ge 4 -and $encoded[0] -eq 0 -and $encoded[1] -eq 0 -and
    (($encoded[2] -eq 1) -or ($encoded[2] -eq 0 -and $encoded[3] -eq 1))
if (-not $annexB) {
    throw "Verification produced no Annex B start code ($($encoded.Length) bytes); the encoder or muxer selection is wrong."
}

$outputDirectory = Join-ProjectPath $OutputDirectory
if (-not (Test-Path -LiteralPath $outputDirectory)) { New-Item -ItemType Directory -Path $outputDirectory | Out-Null }
$target = Join-Path $outputDirectory 'ffmpeg.exe'
Copy-Item -LiteralPath $built -Destination $target -Force
$environment = @{}
$environmentPath = Join-Path $buildOutput 'build.env'
if (Test-Path -LiteralPath $environmentPath -PathType Leaf) {
    foreach ($line in Get-Content -LiteralPath $environmentPath) {
        if ($line -match '^([^=]+)=(.*)$') { $environment[$Matches[1]] = $Matches[2] }
    }
}
$manifest = [ordered]@{
    verified_at = (Get-Date).ToUniversalTime().ToString('o')
    ffmpeg = [ordered]@{ ref = $environment['ffmpeg_ref']; commit = (Get-Content -LiteralPath (Join-Path $buildOutput 'ffmpeg.commit') -Raw).Trim(); source = 'https://git.ffmpeg.org/ffmpeg.git' }
    x264 = [ordered]@{ ref = $environment['x264_ref']; commit = (Get-Content -LiteralPath (Join-Path $buildOutput 'x264.commit') -Raw).Trim(); source = 'https://code.videolan.org/videolan/x264.git' }
    cross_prefix = $environment['cross_prefix']
    configure = (Get-Content -LiteralPath (Join-Path $buildOutput 'configure.txt') -Raw).Trim()
    sha256 = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
    build_script = 'scripts/build-ffmpeg.sh'
    signed = $false
}
$manifestPath = Join-Path $outputDirectory 'ffmpeg-build.json'
Set-Content -LiteralPath $manifestPath -Value ($manifest | ConvertTo-Json -Depth 4) -Encoding UTF8

[pscustomobject]@{
    Path = $target
    SizeMB = [math]::Round((Get-Item -LiteralPath $target).Length / 1MB, 1)
    Sha256 = $manifest.sha256
    Manifest = $manifestPath
} | Format-List
