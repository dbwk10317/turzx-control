# SPDX-License-Identifier: GPL-3.0-or-later
# Read-only installer checks: no elevation, task registration or protected writes.
[CmdletBinding()]
param([string]$HelperDirectory)
$ErrorActionPreference = 'Stop'
if (-not $HelperDirectory) { $HelperDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'artifacts/sensors-task-20260914' }
$setup = Join-Path $PSScriptRoot 'setup-sensor-task.ps1'
$tokens = $null
$parseErrors = $null
[Management.Automation.Language.Parser]::ParseFile($setup, [ref]$tokens, [ref]$parseErrors) | Out-Null
if ($parseErrors.Count) { throw ($parseErrors -join "`n") }

$plan = (& $setup -Action Install -HelperDirectory $HelperDirectory -ValidateOnly | Out-String) | ConvertFrom-Json
if ($plan.action -ne 'Install' -or $plan.file_count -lt 1 -or -not $plan.snapshot.EndsWith('\snapshot.json')) { throw 'Invalid installation plan.' }
$rejected = $false
try { & $setup -Action Install -HelperDirectory $PSScriptRoot -ValidateOnly | Out-Null }
catch { $rejected = $true }
if (-not $rejected) { throw 'Incomplete helper directory was accepted.' }

# Import the same validation functions without performing installation.
. $setup -Action Install -HelperDirectory $HelperDirectory -ValidateOnly | Out-Null
$rejected = $false
try { Assert-ProtectedDirectory ([IO.Path]::GetTempPath()) }
catch { $rejected = $true }
if (-not $rejected) { throw 'Writable temporary directory was accepted as a protected install directory.' }
Write-Output 'Sensor installer read-only checks passed.'
