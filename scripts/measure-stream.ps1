# SPDX-License-Identifier: GPL-3.0-or-later
# G1 Windows probe runner. Records process resources; does not measure panel latency.
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$Probe,
    [Parameter(Mandatory=$true)][string]$Background,
    [Parameter(Mandatory=$true)][string]$FFmpeg,
    [Parameter(Mandatory=$true)][string]$OutputDirectory,
    [ValidateRange(10,7200)][int]$DurationSeconds = 1800
)
$ErrorActionPreference = 'Stop'
$probePath = (Resolve-Path -LiteralPath $Probe).Path
$backgroundPath = (Resolve-Path -LiteralPath $Background).Path
$ffmpegPath = (Resolve-Path -LiteralPath $FFmpeg).Path
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Output directory must be new.' }
$outDir = (New-Item -ItemType Directory -Path $OutputDirectory).FullName
$stdoutPath = Join-Path $outDir 'probe.jsonl'
$stderrPath = Join-Path $outDir 'overlay.jsonl'
$samplePath = Join-Path $outDir 'resources.csv'
$arguments = @('-background', ('"{0}"' -f $backgroundPath), '-ffmpeg', ('"{0}"' -f $ffmpegPath), '-duration', ($DurationSeconds.ToString() + 's'))
$probeStartedAt = Get-Date
$process = Start-Process -FilePath $probePath -ArgumentList $arguments -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
$probeId = $process.Id
# Keep the process handle-backed object from startup for reliable ExitCode access.
$probeHandle = $process.Handle
$encoderId = $null
$encoderHandle = $null
$encoderDiscovered = $false
$clock = [Diagnostics.Stopwatch]::StartNew()
$encoder = $null
$previousCPU = $null
$previousTime = 0.0
$samples = [System.Collections.Generic.List[object]]::new()
try {
while (-not $process.HasExited) {
    if ($clock.Elapsed.TotalSeconds -gt ($DurationSeconds + 30)) {
        # Only terminate process handles created by this run; never name-wide kill.
        throw 'Probe exceeded duration plus 30-second cleanup allowance; processes stopped.'
    }
    if (-not $encoder) {
        $child = Get-CimInstance Win32_Process -Filter "ParentProcessId = $($process.Id)" | Where-Object { $_.Name -eq [IO.Path]::GetFileName($ffmpegPath) } | Select-Object -First 1
        if ($child) {
            $childPathMatches = [StringComparer]::OrdinalIgnoreCase.Equals($child.ExecutablePath, $ffmpegPath)
            $childStartedAt = [Management.ManagementDateTimeConverter]::ToDateTime($child.CreationDate)
            if ($childPathMatches -and $childStartedAt -ge $probeStartedAt) {
                $encoderId = $child.ProcessId
                $encoder = Get-Process -Id $encoderId -ErrorAction SilentlyContinue
                if ($encoder) { $encoderHandle = $encoder.Handle }
                $encoderDiscovered = $null -ne $encoder
            }
        }
    }
    $process.Refresh()
    if ($encoder) { $encoder.Refresh() }
    if ($encoder -and -not $encoder.HasExited -and -not $process.HasExited) {
        $seconds = $clock.Elapsed.TotalSeconds
        $cpu = $process.TotalProcessorTime.TotalSeconds + $encoder.TotalProcessorTime.TotalSeconds
        $load = $null
        if ($null -ne $previousCPU -and $seconds -gt $previousTime) {
            $load = 100 * ($cpu - $previousCPU) / ($seconds - $previousTime) / [Environment]::ProcessorCount
        }
        $sample = [pscustomobject]@{
            elapsed_seconds = [Math]::Round($seconds,3)
            cpu_percent_host = $load
            working_set_mib = ($process.WorkingSet64 + $encoder.WorkingSet64) / 1MB
            private_mib = ($process.PrivateMemorySize64 + $encoder.PrivateMemorySize64) / 1MB
        }
        $samples.Add($sample)
        $sample | Export-Csv -LiteralPath $samplePath -NoTypeInformation -Append -Encoding UTF8
        $previousCPU = $cpu
        $previousTime = $seconds
    }
    Start-Sleep -Seconds 5
    $process.Refresh()
}
$process.WaitForExit()
$process.Refresh()
$probeExitCode = $null
$probeExitCodeReason = $null
try { $probeExitCode = $process.ExitCode } catch { $probeExitCodeReason = 'exit_code_unavailable' }
if ($null -eq $probeExitCode -and $null -eq $probeExitCodeReason) { $probeExitCodeReason = 'exit_code_unavailable' }
$warm = @($samples | Where-Object { $_.elapsed_seconds -ge 30 -and $null -ne $_.cpu_percent_host })
$summary = [ordered]@{
    duration_seconds = $clock.Elapsed.TotalSeconds
    probe_exit_code = $probeExitCode
    probe_exit_code_status = if ($null -eq $probeExitCode) { $probeExitCodeReason } else { 'available' }
    samples = $samples.Count
    encoder_discovered = $encoderDiscovered
    warmup_seconds = 30
    cpu_mean_percent_host = ($warm | Measure-Object cpu_percent_host -Average).Average
    working_set_peak_mib = ($warm | Measure-Object working_set_mib -Maximum).Maximum
    private_peak_mib = ($warm | Measure-Object private_mib -Maximum).Maximum
    encoder_exited = ($null -eq $encoder -or $encoder.HasExited)
    panel_latency_measured = $false
}
$summary | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $outDir 'resources-summary.json') -Encoding UTF8
$summary | ConvertTo-Json
} finally {
    # Only terminate process IDs created or discovered by this run; never name-wide kill.
    if (-not $encoder) {
        try {
            $child = Get-CimInstance Win32_Process -Filter "ParentProcessId = $probeId" |
                Where-Object { $_.Name -eq [IO.Path]::GetFileName($ffmpegPath) } |
                Select-Object -First 1
            if ($child) {
                $childPathMatches = [StringComparer]::OrdinalIgnoreCase.Equals($child.ExecutablePath, $ffmpegPath)
                $childStartedAt = [Management.ManagementDateTimeConverter]::ToDateTime($child.CreationDate)
                if ($childPathMatches -and $childStartedAt -ge $probeStartedAt) {
                    $encoderId = $child.ProcessId
                    $encoder = Get-Process -Id $encoderId -ErrorAction SilentlyContinue
                    if ($encoder) { $encoderHandle = $encoder.Handle }
                }
            }
        } catch { }
    }
    if ($encoder -and -not $encoder.HasExited) {
        try { $encoder.Kill() } catch { }
        try { [void]$encoder.WaitForExit(5000) } catch { }
    }
    if ($process -and -not $process.HasExited) {
        try { $process.Kill() } catch { }
        try { [void]$process.WaitForExit(5000) } catch { }
    }
}
