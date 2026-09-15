# SPDX-License-Identifier: GPL-3.0-or-later
# Assemble the standalone ZIP: control app, sensor helper, installer and the
# license notices for everything bundled. Development builds are unsigned and
# are not a release; sign the executables before treating the output as one.
[CmdletBinding()]
param(
    # Payload only: build outputs and packages live beside it, not inside it.
    [string]$AppDirectory = 'bin/app',
    [Parameter(Mandatory = $true)][string]$HelperDirectory,
    # The theme background to ship. Passed explicitly and copied in after the
    # payload, so a personal, non-redistributable theme sitting in the local
    # run directory can never reach a package.
    [Parameter(Mandatory = $true)][string]$Background,
    # The build-ffmpeg.sh output directory for the bundled ffmpeg.exe: it carries
    # the license texts and the commits of the exact sources it was built from,
    # which is what the GPL corresponding source offer has to match.
    [Parameter(Mandatory = $true)][string]$FFmpegBuildOutput,
    # Bundled LGPL binaries ship with their own license text; pass the copy that
    # belongs to the exact build being packaged, not a generic one.
    [Parameter(Mandatory = $true)][string]$LibusbLicense,
    # How the recipient obtains the corresponding source for the GPL parts.
    # Required: a GPL binary distribution without this offer is not compliant.
    [Parameter(Mandatory = $true)][string]$SourceOffer,
    [string]$DotnetRoot = '.tools/dotnet',
    [string]$NuGetRoot = '.tools/nuget',
    [string]$GoModCache,
    [Parameter(Mandatory = $true)][string]$OutputPath
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot

function Resolve-Input([string]$Path, [string]$Label) {
    if (-not [IO.Path]::IsPathRooted($Path)) { $Path = Join-Path $projectRoot $Path }
    if (-not (Test-Path -LiteralPath $Path)) { throw "$Label does not exist: $Path" }
    return (Resolve-Path -LiteralPath $Path).Path.TrimEnd('\')
}

$AppDirectory = Resolve-Input $AppDirectory 'App payload'
$HelperDirectory = Resolve-Input $HelperDirectory 'Sensor helper publish'
$LibusbLicense = Resolve-Input $LibusbLicense 'libusb license'
$FFmpegBuildOutput = Resolve-Input $FFmpegBuildOutput 'FFmpeg build output'
$Background = Resolve-Input $Background 'Theme background'
if ([IO.Path]::GetExtension($Background) -ne '.mp4') { throw "Theme background must be an .mp4 file: $Background" }
foreach ($name in @('ffmpeg.exe', 'ffmpeg.commit', 'x264.commit', 'configure.txt', 'ffmpeg-COPYING.GPLv3.txt', 'x264-COPYING.txt')) {
    if (-not (Test-Path -LiteralPath (Join-Path $FFmpegBuildOutput $name) -PathType Leaf)) {
        throw "FFmpeg build output is missing ${name}: $FFmpegBuildOutput"
    }
}
$DotnetRoot = Resolve-Input $DotnetRoot '.NET root'
$NuGetRoot = Resolve-Input $NuGetRoot 'NuGet package root'
if (-not $GoModCache) { $GoModCache = (& go env GOMODCACHE) }
if (-not $GoModCache -or -not (Test-Path -LiteralPath $GoModCache)) { throw "Go module cache not found: $GoModCache" }
if (-not [IO.Path]::IsPathRooted($OutputPath)) { $OutputPath = Join-Path $projectRoot $OutputPath }
$OutputPath = [IO.Path]::GetFullPath($OutputPath)
if (Test-Path -LiteralPath $OutputPath) { throw "Output already exists; choose a new -OutputPath: $OutputPath" }

foreach ($required in @('turzx-control.exe', 'turzx-claude-status.exe', 'ffmpeg.exe', 'libusb-1.0.dll')) {
    if (-not (Test-Path -LiteralPath (Join-Path $AppDirectory $required) -PathType Leaf)) { throw "App payload is missing $required" }
}

if (-not (Test-Path -LiteralPath (Join-Path $HelperDirectory 'turzx-sensors.exe') -PathType Leaf)) { throw 'Sensor helper publish is missing turzx-sensors.exe' }
# The payload is copied whole, so a build scratch directory left inside it would
# ship silently. It is flat today; keep it that way rather than guess intent.
$nested = Get-ChildItem -LiteralPath $AppDirectory -Directory
if ($nested) {
    throw "App payload must contain no subdirectories; found $($nested.Name -join ', ') in $AppDirectory"
}

$staging = Join-Path ([IO.Path]::GetTempPath()) ('turzx-package-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
$payload = New-Item -ItemType Directory -Path (Join-Path $staging 'turzx-control')
try {
    $app = Join-Path $payload 'App'
    Copy-Item -LiteralPath $AppDirectory -Destination $app -Recurse
    # Only the background passed on the command line ships.
    Get-ChildItem -LiteralPath $app -Filter '*.mp4' -File | Remove-Item -Force
    Copy-Item -LiteralPath $Background -Destination $app
    Copy-Item -LiteralPath $HelperDirectory -Destination (Join-Path $payload 'Sensors') -Recurse
    # The installer pins the reviewed helper's manifest hash, so shipping both
    # together makes the ZIP verify its own sensor payload on install.
    $scripts = New-Item -ItemType Directory -Path (Join-Path $payload 'scripts')
    foreach ($script in @('setup-sensor-task.ps1', 'sensor-acl.psm1')) {
        Copy-Item -LiteralPath (Join-Path $PSScriptRoot $script) -Destination $scripts
    }
    Copy-Item -LiteralPath (Join-Path $projectRoot 'LICENSE') -Destination (Join-Path $payload 'LICENSE')

    $notices = New-Item -ItemType Directory -Path (Join-Path $payload 'NOTICES')
    $index = [Collections.Generic.List[string]]::new()
    $index.Add('TURZX Control bundled components and their license texts.')
    $index.Add('')
    $index.Add('turzx-control, turzx-claude-status, turzx-sensors: GPL-3.0-or-later, see LICENSE.')
    $index.Add('')
    # The notices must describe the binary that actually ships, so the payload's
    # ffmpeg.exe has to be the one this build output produced.
    $payloadFFmpeg = (Get-FileHash -LiteralPath (Join-Path $app 'ffmpeg.exe') -Algorithm SHA256).Hash
    $builtFFmpeg = (Get-FileHash -LiteralPath (Join-Path $FFmpegBuildOutput 'ffmpeg.exe') -Algorithm SHA256).Hash
    if ($payloadFFmpeg -ne $builtFFmpeg) {
        throw 'App payload ffmpeg.exe differs from the build output; run build-ffmpeg.ps1 to install the verified binary first.'
    }
    foreach ($name in @('ffmpeg-COPYING.GPLv3.txt', 'ffmpeg-LICENSE.md', 'x264-COPYING.txt')) {
        $source = Join-Path $FFmpegBuildOutput $name
        if (Test-Path -LiteralPath $source -PathType Leaf) { Copy-Item -LiteralPath $source -Destination (Join-Path $notices $name) }
    }
    $index.Add('FFmpeg (App/ffmpeg.exe): NOTICES/ffmpeg-COPYING.GPLv3.txt, NOTICES/ffmpeg-LICENSE.md')
    $index.Add('x264, statically linked into App/ffmpeg.exe: NOTICES/x264-COPYING.txt')
    # GPL corresponding source is only identifiable against the exact build.
    $buildInfo = @('FFmpeg built from source for this product; see scripts/build-ffmpeg.sh in the source distribution.', '')
    $buildInfo += 'ffmpeg commit: ' + (Get-Content -LiteralPath (Join-Path $FFmpegBuildOutput 'ffmpeg.commit') -Raw).Trim() + ' (https://git.ffmpeg.org/ffmpeg.git)'
    $buildInfo += 'x264 commit:   ' + (Get-Content -LiteralPath (Join-Path $FFmpegBuildOutput 'x264.commit') -Raw).Trim() + ' (https://code.videolan.org/videolan/x264.git)'
    $buildInfo += ''
    $buildInfo += 'configure: ' + (Get-Content -LiteralPath (Join-Path $FFmpegBuildOutput 'configure.txt') -Raw).Trim()
    $buildInfo += ''
    $buildInfo += & (Join-Path $app 'ffmpeg.exe') -hide_banner -version 2>&1
    Set-Content -LiteralPath (Join-Path $notices 'ffmpeg-BUILD.txt') -Value $buildInfo -Encoding UTF8
    $index.Add('FFmpeg build and source identification: NOTICES/ffmpeg-BUILD.txt')
    Copy-Item -LiteralPath $LibusbLicense -Destination (Join-Path $notices 'libusb-COPYING.txt')
    $index.Add('libusb (App/libusb-1.0.dll): NOTICES/libusb-COPYING.txt')
    foreach ($pair in @(@('LICENSE.txt', 'dotnet-LICENSE.txt'), @('ThirdPartyNotices.txt', 'dotnet-ThirdPartyNotices.txt'))) {
        $source = Join-Path $DotnetRoot $pair[0]
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "Bundled .NET runtime notice is missing: $source" }
        Copy-Item -LiteralPath $source -Destination (Join-Path $notices $pair[1])
    }
    $index.Add('.NET runtime (Sensors/): NOTICES/dotnet-LICENSE.txt, NOTICES/dotnet-ThirdPartyNotices.txt')
    $index.Add('')

    # Go modules: the module cache holds each module's own license text.
    $goNotices = New-Item -ItemType Directory -Path (Join-Path $notices 'go')
    $index.Add('Go modules linked into the control app:')
    foreach ($line in Get-Content -LiteralPath (Join-Path $projectRoot 'go.mod')) {
        if ($line -notmatch '^\s+(\S+)\s+(v\S+)') { continue }
        $module, $version = $Matches[1], $Matches[2]
        # Upper-case path elements are escaped as "!" plus the lower-case letter.
        $escaped = [regex]::Replace($module, '[A-Z]', { '!' + $args[0].Value.ToLowerInvariant() })
        $directory = Join-Path $GoModCache ($escaped + '@' + $version)
        $license = Get-ChildItem -LiteralPath $directory -File -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -match '^(LICENSE|LICENCE|COPYING)(\.\w+)?$' } | Select-Object -First 1
        if (-not $license) { throw "No license file found for Go module ${module}@${version} in $directory" }
        $target = ($module -replace '[\\/]', '_') + '@' + $version + '.txt'
        Copy-Item -LiteralPath $license.FullName -Destination (Join-Path $goNotices $target)
        $index.Add("  ${module} ${version}: NOTICES/go/$target")
    }
    $index.Add('')

    # NuGet packages: license file when the package embeds one, otherwise the
    # SPDX expression the .nuspec declares.
    $nugetNotices = New-Item -ItemType Directory -Path (Join-Path $notices 'nuget')
    $index.Add('NuGet packages linked into the sensor helper:')
    foreach ($spec in Get-ChildItem -LiteralPath $NuGetRoot -Recurse -Filter '*.nuspec' -File) {
        $xml = [xml](Get-Content -LiteralPath $spec.FullName -Raw)
        $metadata = $xml.package.metadata
        $license = $metadata.license
        if ($license -and $license.type -eq 'file' -and $license.'#text') {
            $embedded = Join-Path $spec.DirectoryName $license.'#text'
            if (Test-Path -LiteralPath $embedded -PathType Leaf) {
                $target = $metadata.id + '-' + $metadata.version + '.txt'
                Copy-Item -LiteralPath $embedded -Destination (Join-Path $nugetNotices $target)
                $index.Add(("  {0} {1}: NOTICES/nuget/{2}" -f $metadata.id, $metadata.version, $target))
                continue
            }
        }
        $expression = if ($license -and $license.'#text') { $license.'#text' } elseif ($metadata.licenseUrl) { $metadata.licenseUrl } else { $null }
        if (-not $expression) { throw "NuGet package $($metadata.id) $($metadata.version) declares no license" }
        $index.Add(("  {0} {1}: {2}" -f $metadata.id, $metadata.version, $expression))
    }
    $index.Add('')
    $index.Add('Corresponding source for the GPL and LGPL components:')
    $index.Add("  $SourceOffer")
    Set-Content -LiteralPath (Join-Path $notices 'INDEX.txt') -Value $index -Encoding UTF8

    $readme = @(
        'TURZX Control (개발 빌드, 서명되지 않음)',
        '',
        '압축을 푼 뒤 첫 실행에서 배경 영상과 테마를 지정해 저장한다. 동봉한 테마는',
        'azure-ribbon이며, <경로>는 압축을 푼 자리로 바꾼다.',
        '',
        '  <경로>\App\turzx-control.exe -theme azure-ribbon -background "<경로>\App\azure-ribbon.mp4" -ffmpeg "<경로>\App\ffmpeg.exe" -save-config',
        '',
        '이후에는 App\turzx-control.exe만 실행하면 저장된 설정을 쓴다.',
        '',
        '하드웨어 온도 센서를 쓰려면 관리자 승인이 필요한 설치를 한 번 수행한다.',
        '앱과 helper, snapshot을 한 디렉터리에 모으려면:',
        '',
        '  powershell.exe -NoProfile -File .\scripts\setup-sensor-task.ps1 -Action Install -HelperDirectory .\Sensors -AppDirectory .\App -InstallRoot C:\TURZX',
        '',
        '설치 루트와 그 상위 디렉터리는 관리자 소유여야 한다. 제거는 같은 -InstallRoot로',
        '-Action Remove를 실행한다. 설치한 파일과 예약 작업을 지우며, 다른 앱과 공유하는',
        'PawnIO 드라이버는 남긴다.',
        '',
        '동봉한 구성 요소의 라이선스는 NOTICES\INDEX.txt에 있다. 이 빌드는 서명되지',
        '않았으므로 정식 배포본이 아니다.'
    )
    Set-Content -LiteralPath (Join-Path $payload 'INSTALL.txt') -Value $readme -Encoding UTF8

    $manifest = Get-ChildItem -LiteralPath $payload -Recurse -File | ForEach-Object {
        $_.FullName.Substring($payload.FullName.Length + 1) + ':' + (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
    }
    Set-Content -LiteralPath (Join-Path $payload 'manifest.sha256') -Value ($manifest | Sort-Object) -Encoding UTF8

    $outputParent = Split-Path -Parent $OutputPath
    if (-not (Test-Path -LiteralPath $outputParent)) { New-Item -ItemType Directory -Path $outputParent | Out-Null }
    Compress-Archive -Path $payload.FullName -DestinationPath $OutputPath -CompressionLevel Optimal
}
finally {
    Remove-Item -LiteralPath $staging -Recurse -Force -ErrorAction SilentlyContinue
}

$archive = Get-Item -LiteralPath $OutputPath
# Report what the payload actually carries rather than assuming. A self-signed
# certificate reports UnknownError here: the signature is present but its chain
# is not trusted on this machine.
$signatures = foreach ($name in @('turzx-control.exe', 'turzx-claude-status.exe', 'ffmpeg.exe')) {
    Get-AuthenticodeSignature -LiteralPath (Join-Path $AppDirectory $name)
}
$unsigned = @($signatures | Where-Object { $_.Status -eq 'NotSigned' }).Count
[pscustomobject]@{
    Path = $archive.FullName
    SizeMB = [math]::Round($archive.Length / 1MB, 1)
    Sha256 = (Get-FileHash -LiteralPath $archive.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    Signed = ($unsigned -eq 0)
    Signer = ($signatures | Where-Object { $_.SignerCertificate } | Select-Object -First 1).SignerCertificate.Subject
    TrustedHere = @($signatures | Where-Object { $_.Status -eq 'Valid' }).Count -eq $signatures.Count
} | Format-List
