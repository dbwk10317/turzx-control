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
$pawn = $null
if ($IncludePawnIO) {
    $fetchScript = Join-Path $PSScriptRoot 'fetch-pawnio.ps1'
    # fetch-pawnio.ps1 outputs the verified object (Path, Sha256, SignerSubject).
    if ($PawnIOSetupPath) {
        $pawn = (& $fetchScript -VerifyOnly -Path $PawnIOSetupPath | Select-Object -Last 1)
    }
    else {
        $pawn = (& $fetchScript | Select-Object -Last 1)
    }
    if (-not $pawn.Path) { throw 'PawnIO fetch/verification failed' }
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
# Capture the output: the helper is a WinExe, so the shell waits for it only
# when its output is redirected.
$selfTest = & $executable --self-test | Out-String
if ($LASTEXITCODE -ne 0) { throw "Sensor helper self-test failed ($LASTEXITCODE)" }
Write-Output $selfTest.Trim()

if ($IncludePawnIO) {
    $driversPath = Join-Path $OutputPath 'drivers'
    New-Item -ItemType Directory -Path $driversPath | Out-Null
    $publishedPawnPath = Join-Path $driversPath 'PawnIO_setup.exe'
    Copy-Item -LiteralPath $pawn.Path -Destination $publishedPawnPath
    $published = (& $fetchScript -VerifyOnly -Path $publishedPawnPath | Select-Object -Last 1)
    if (-not $published.Path) { throw 'Published PawnIO verification failed' }
    [pscustomobject]@{
        pawnio = [pscustomobject]@{
            version = '2.2.0'
            sourceUrl = 'https://github.com/namazso/PawnIO.Setup/releases/download/2.2.0/PawnIO_setup.exe'
            sha256 = $published.Sha256
            signerSubject = $published.SignerSubject
        }
    } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $OutputPath 'components.json') -Encoding UTF8
}
Write-Output "Self-contained sensor helper: $executable"
