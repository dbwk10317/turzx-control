# SPDX-License-Identifier: GPL-3.0-or-later
# Read-only installer checks: no elevation, task registration or protected writes.
[CmdletBinding()]
param([string]$HelperDirectory)
$ErrorActionPreference = 'Stop'
Import-Module "$PSScriptRoot\sensor-acl.psm1" -Force
if (-not $HelperDirectory) { $HelperDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'artifacts/sensors-task-20260914-owner' }
$setup = Join-Path $PSScriptRoot 'setup-sensor-task.ps1'
$tokens = $null
$parseErrors = $null
[Management.Automation.Language.Parser]::ParseFile($setup, [ref]$tokens, [ref]$parseErrors) | Out-Null
if ($parseErrors.Count) { throw ($parseErrors -join "`n") }

$plan = (& $setup -Action Install -HelperDirectory $HelperDirectory -ValidateOnly | Out-String) | ConvertFrom-Json
if ($plan.action -ne 'Install' -or $plan.file_count -lt 1 -or -not $plan.snapshot.EndsWith('\snapshot.json')) { throw 'Invalid installation plan.' }
$exitCode = 0
if (-not $plan.manifest_pinned) {
    Write-Warning "Helper manifest hash $($plan.manifest_hash) differs from the pinned hash in setup-sensor-task.ps1; installation would be refused."
    $exitCode = 1
}
$rejected = $false
try { & $setup -Action Install -HelperDirectory $PSScriptRoot -ValidateOnly | Out-Null }
catch { $rejected = $true }
if (-not $rejected) { throw 'Incomplete helper directory was accepted.' }

# A custom -InstallRoot keeps the helper and the snapshot under one directory.
$root = Join-Path $env:ProgramData 'TURZXControl-installroot-check'
$rooted = (& $setup -Action Install -HelperDirectory $HelperDirectory -InstallRoot $root -ValidateOnly | Out-String) | ConvertFrom-Json
if ($rooted.install_root -ne $root -or -not $rooted.snapshot.StartsWith($root + '\')) { throw 'InstallRoot was not applied to the installation plan.' }
if ($rooted.destination -and -not $rooted.destination.StartsWith($root + '\')) { throw 'Helper destination left the install root.' }

# -AppDirectory adds the control app to that same root.
$app = Join-Path (Split-Path -Parent $PSScriptRoot) 'bin'
if (Test-Path -LiteralPath (Join-Path $app 'turzx-control.exe')) {
    $withApp = (& $setup -Action Install -HelperDirectory $HelperDirectory -AppDirectory $app -InstallRoot $root -ValidateOnly | Out-String) | ConvertFrom-Json
    if ($withApp.app -ne (Join-Path $root 'App\turzx-control.exe') -or $withApp.app_file_count -lt 1) { throw 'AppDirectory was not applied to the installation plan.' }
}
$rejected = $false
try { & $setup -Action Install -HelperDirectory $HelperDirectory -AppDirectory $PSScriptRoot -InstallRoot $root -ValidateOnly | Out-Null }
catch { $rejected = $true }
if (-not $rejected) { throw 'App payload without turzx-control.exe was accepted.' }

$rejected = $false
try { & $setup -Action Install -HelperDirectory $HelperDirectory -InstallRoot (Join-Path ([IO.Path]::GetTempPath()) 'turzx-root') -ValidateOnly | Out-Null }
catch { $rejected = $true }
if (-not $rejected) { throw 'Install root under a user-writable ancestor was accepted.' }

$rejected = $false
try { Assert-ProtectedDirectory ([IO.Path]::GetTempPath()) }
catch { $rejected = $true }
if (-not $rejected) { throw 'Writable temporary directory was accepted as a protected install directory.' }
if ($exitCode -eq 0) { Write-Output 'Sensor installer read-only checks passed.' }
exit $exitCode
