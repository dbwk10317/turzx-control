# SPDX-License-Identifier: GPL-3.0-or-later
# Read-only G0 inventory for turzx-control. No driver or account changes.
[CmdletBinding()]
param(
    [string]$FFmpegPath,
    [string]$MsysRoot
)

$ErrorActionPreference = 'Stop'
$problems = [System.Collections.Generic.List[string]]::new()

function Find-Tool([string]$Name, [string]$Fallback) {
    $command = Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($command) { return $command.Source }
    if ($Fallback -and (Test-Path -LiteralPath $Fallback)) { return $Fallback }
    return $null
}

$osInfo = $null
$cpuInfo = @()
$gpuInfo = @()
try {
    $os = Get-CimInstance Win32_OperatingSystem
    $osInfo = [ordered]@{ name = $os.Caption; version = $os.Version; architecture = $os.OSArchitecture }
    $cpuInfo = @(Get-CimInstance Win32_Processor | ForEach-Object {
        [ordered]@{ name = $_.Name; logical_processors = $_.NumberOfLogicalProcessors }
    })
    $gpuInfo = @(Get-CimInstance Win32_VideoController | ForEach-Object {
        [ordered]@{ name = $_.Name; driver_version = $_.DriverVersion }
    })
} catch {
    $problems.Add("OS/hardware inventory failed: $($_.Exception.Message)")
}

$devices = @()
$usbQuerySucceeded = $false
try {
    $devices = @(Get-PnpDevice -PresentOnly | Where-Object {
        $_.InstanceId -like 'USB\VID_1CBE&PID_0092*'
    } | ForEach-Object {
        $device = $_
        $service = $null
        $driver = $null
        try {
            $service = (Get-PnpDeviceProperty -InstanceId $device.InstanceId -KeyName 'DEVPKEY_Device_Service').Data
            $driver = (Get-PnpDeviceProperty -InstanceId $device.InstanceId -KeyName 'DEVPKEY_Device_DriverInfPath').Data
        } catch {
            $problems.Add("USB driver properties unavailable: $($_.Exception.Message)")
        }
        # Omit the instance suffix, which can contain a device serial number.
        [ordered]@{
            name = $device.FriendlyName
            status = [string]$device.Status
            service = $service
            driver_inf = $driver
        }
    })
    $usbQuerySucceeded = $true
} catch {
    $problems.Add("USB inventory failed: $($_.Exception.Message)")
}

$toolPaths = [ordered]@{
    go = Find-Tool 'go' 'C:\Program Files\Go\bin\go.exe'
    ffmpeg = Find-Tool 'ffmpeg' $FFmpegPath
    gcc = Find-Tool 'gcc' $(if ($MsysRoot) { Join-Path $MsysRoot 'ucrt64\bin\gcc.exe' })
    pkg_config = Find-Tool 'pkg-config' $(if ($MsysRoot) { Join-Path $MsysRoot 'ucrt64\bin\pkg-config.exe' })
}

[ordered]@{
    schema_version = 1
    observed_at = [DateTimeOffset]::UtcNow.ToString('o')
    os = $osInfo
    cpu = $cpuInfo
    gpu = $gpuInfo
    tools = $toolPaths
    usb_query_succeeded = $usbQuerySucceeded
    usb_devices = $devices
    errors = @($problems.ToArray())
    remaining_checks = @(
        'libusb loading and standard-user USB open/drain/sync (inventory does not prove access)'
        'ffmpeg version, filters, encoders and supplied local motion asset'
        'LHM sensor IDs, numeric fields and local-only listener'
        'Windows/WSL inbox atomic replacement, concurrent writes and WSL shutdown'
        'Codex non-conversational query and account identity before/after switching'
        'G1 continuous stream and physical display latency'
    )
} | ConvertTo-Json -Depth 6

if ($problems.Count -gt 0) { exit 1 }
