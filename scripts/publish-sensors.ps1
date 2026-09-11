# SPDX-License-Identifier: GPL-3.0-or-later
# Publish our sensor helper with its .NET runtime; no system installation.
[CmdletBinding()]
param(
    [string]$DotnetPath = 'dotnet',
    [string]$OutputPath = 'artifacts/sensors-win-x64',
    [switch]$NoRestore,
    [switch]$IncludePawnIO,
    [string]$PawnIOSetupPath
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$pawnPath = $null
if ($IncludePawnIO) {
    $fetchScript = Join-Path $PSScriptRoot 'fetch-pawnio.ps1'
    if ($PawnIOSetupPath) {
        $pawnPath = (& $fetchScript -VerifyOnly -Path $PawnIOSetupPath | Select-Object -Last 1)
    }
    else {
        $pawnPath = (& $fetchScript | Select-Object -Last 1)
    }
    if (-not $pawnPath) { throw 'PawnIO fetch/verification failed' }
    $pawnPath = [System.IO.Path]::GetFullPath([string]$pawnPath)
}
if (-not [System.IO.Path]::IsPathRooted($OutputPath)) {
    $OutputPath = Join-Path $projectRoot $OutputPath
}
$OutputPath = [System.IO.Path]::GetFullPath($OutputPath)
if (Test-Path -LiteralPath $OutputPath) {
    throw "Output already exists; choose a new -OutputPath: $OutputPath"
}
$project = Join-Path $projectRoot 'tools/turzx-sensors/turzx-sensors.csproj'
$publishArgs = @('publish', $project, '--configuration', 'Release', '--runtime', 'win-x64',
    '--self-contained', 'true', '--output', $OutputPath, '-p:RestoreLockedMode=true')
if ($NoRestore) { $publishArgs += '--no-restore' }
& $DotnetPath @publishArgs
if ($LASTEXITCODE -ne 0) { throw "Sensor helper publish failed ($LASTEXITCODE)" }

$executable = Join-Path $OutputPath 'turzx-sensors.exe'
& $executable --self-test
if ($LASTEXITCODE -ne 0) { throw "Sensor helper self-test failed ($LASTEXITCODE)" }

if ($IncludePawnIO) {
    $driversPath = Join-Path $OutputPath 'drivers'
    New-Item -ItemType Directory -Path $driversPath | Out-Null
    $publishedPawnPath = Join-Path $driversPath 'PawnIO_setup.exe'
    Copy-Item -LiteralPath $pawnPath -Destination $publishedPawnPath
    & $fetchScript -VerifyOnly -Path $publishedPawnPath | Out-Null
    $hash = (Get-FileHash -LiteralPath $publishedPawnPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $signature = Get-AuthenticodeSignature -LiteralPath $publishedPawnPath
    [pscustomobject]@{
        pawnio = [pscustomobject]@{
            version = '2.2.0'
            sourceUrl = 'https://github.com/namazso/PawnIO.Setup/releases/download/2.2.0/PawnIO_setup.exe'
            sha256 = $hash
            signerSubject = [string]$signature.SignerCertificate.Subject
        }
    } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $OutputPath 'components.json') -Encoding UTF8
}
Write-Output "Self-contained sensor helper: $executable"
