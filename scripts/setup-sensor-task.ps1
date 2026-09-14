# SPDX-License-Identifier: GPL-3.0-or-later
# Install a protected, read-only sensor publisher. Never elevate the control app.
# Run as the Windows user that runs TURZX Control. The UAC prompt must be answered
# with that same user's administrator (split) token: the task runs under the
# caller's own SID, so elevating as a different administrator account is refused.
[CmdletBinding()]
param(
    [ValidateSet('Install', 'Remove', 'Status')][string]$Action = 'Status',
    [string]$HelperDirectory,
    [string]$UserSid = ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value),
    [switch]$ValidateOnly,
    [switch]$ElevatedStage
)

$ErrorActionPreference = 'Stop'
# Pinned, locally built development helper (artifacts/sensors-task-20260914).
# Update only after reviewing and verifying a new self-contained publish. The
# installer script itself must come from the trusted source checkout/package.
$expectedManifestHash = 'bfec915a2105ecece1bc1b3c3b959e0141532bcfbcfdd79260509ff0517fe8c9'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
$administrator = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$sid = [Security.Principal.SecurityIdentifier]::new($UserSid)
if ($sid.Value -ne $identity.User.Value) { throw 'Run setup as the same Windows user that runs TURZX Control; the elevated identity must be that user''s own administrator (split) token, not another administrator account, because the task runs under the caller''s SID.' }
$taskName = 'TURZX Sensors ' + $sid.Value
$programRoot = Join-Path $env:ProgramFiles 'TURZXControl'
$dataRoot = Join-Path $env:ProgramData 'TURZXControl'
$sensorRoot = Join-Path $programRoot 'Sensors'
$userData = Join-Path (Join-Path $dataRoot 'Sensors') $sid.Value
$snapshotPath = Join-Path $userData 'snapshot.json'
$recordPath = Join-Path $userData 'installation.json'
# Assert-NoReparse, Assert-ProtectedDirectory, New-ProtectedDirectory, $adminSid, $systemSid, $usersSid.
Import-Module "$PSScriptRoot\sensor-acl.psm1" -Force

function Get-OwnedTask {
    $queryErrors = @()
    $task = Get-ScheduledTask -TaskPath '\' -TaskName $taskName -ErrorAction SilentlyContinue -ErrorVariable queryErrors
    foreach ($queryError in $queryErrors) {
        if ($queryError.CategoryInfo.Category -ne 'ObjectNotFound') { throw $queryError }
    }
    if (-not $task) { return $null }
    if (-not (Test-Path -LiteralPath $recordPath -PathType Leaf)) { throw "Existing task has no TURZX installation record: $taskName" }
    Assert-ProtectedDirectory $userData
    Assert-NoReparse $recordPath
    $record = Get-Content -LiteralPath $recordPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $expectedArgs = '--snapshot-file "' + $snapshotPath + '"'
    $taskSid = $task.Principal.UserId
    if ($taskSid -and -not $taskSid.StartsWith('S-1-')) {
        $taskSid = ([Security.Principal.NTAccount]::new($taskSid)).Translate([Security.Principal.SecurityIdentifier]).Value
    }
    if ($record.user_sid -ne $sid.Value -or $record.task_name -ne $taskName -or
        $task.Actions.Count -ne 1 -or $task.Actions[0].Execute -ne $record.executable -or
        $task.Actions[0].Arguments -ne $expectedArgs -or $task.Principal.RunLevel -ne 'Highest' -or
        $task.Principal.LogonType -ne 'Interactive' -or $taskSid -ne $sid.Value -or
        $task.Triggers.Count -ne 1 -or $task.Triggers[0].CimClass.CimClassName -ne 'MSFT_TaskLogonTrigger' -or
        $task.Settings.MultipleInstances -ne 'IgnoreNew' -or $task.Settings.ExecutionTimeLimit -ne 'PT0S') {
        throw "Existing task differs from the managed TURZX sensor task: $taskName"
    }
    $triggerSid = $task.Triggers[0].UserId
    if ($triggerSid -and -not $triggerSid.StartsWith('S-1-')) {
        $triggerSid = ([Security.Principal.NTAccount]::new($triggerSid)).Translate([Security.Principal.SecurityIdentifier]).Value
    }
    if ($triggerSid -ne $sid.Value) { throw 'Sensor task logon trigger belongs to another user.' }
    $service = New-Object -ComObject Schedule.Service
    $service.Connect()
    $taskSecurity = [Security.AccessControl.RawSecurityDescriptor]::new($service.GetFolder('\').GetTask($taskName).GetSecurityDescriptor(5))
    if ($taskSecurity.Owner.Value -notin @($adminSid.Value, $systemSid.Value)) { throw 'Sensor task is not owned by administrators or SYSTEM.' }
    if (-not ($taskSecurity.ControlFlags -band [Security.AccessControl.ControlFlags]::DiscretionaryAclProtected) -or $taskSecurity.DiscretionaryAcl.Count -ne 3) { throw 'Sensor task security differs from the managed task.' }
    $taskSids = @($taskSecurity.DiscretionaryAcl | ForEach-Object { $_.SecurityIdentifier.Value } | Sort-Object -Unique)
    if ($taskSids.Count -ne 3 -or $sid.Value -notin $taskSids -or $adminSid.Value -notin $taskSids -or $systemSid.Value -notin $taskSids) { throw 'Missing expected sensor task principal.' }
    foreach ($ace in $taskSecurity.DiscretionaryAcl) {
        if ($ace.AceType -ne 'AccessAllowed' -or $ace.AceFlags -ne 'None') { throw 'Unexpected sensor task permission rule.' }
        if ($ace.SecurityIdentifier.Value -in @($adminSid.Value, $systemSid.Value)) {
            if ($ace.AccessMask -notin @(0x10000000, 0x1F01FF)) { throw 'Unexpected administrator task rights.' }
        } elseif ($ace.SecurityIdentifier.Value -eq $sid.Value) {
            if ($ace.AccessMask -notin @(-1610612736, 0x1200A9)) { throw 'Unexpected user task rights.' }
        } else { throw 'Unexpected sensor task principal.' }
    }
    $exePath = [IO.Path]::GetFullPath($record.executable)
    if (-not $exePath.StartsWith($sensorRoot + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Task executable is outside the protected sensor directory.' }
    Assert-NoReparse $exePath
    return $task
}

function Show-Status {
    $task = Get-OwnedTask
    $result = [ordered]@{ task_name = $taskName; registered = [bool]$task; snapshot = $snapshotPath }
    if ($task) {
        $result.state = [string]$task.State
        $result.last_result = (Get-ScheduledTaskInfo -TaskName $taskName -TaskPath '\').LastTaskResult
    }
    if (Test-Path -LiteralPath $snapshotPath -PathType Leaf) {
        Assert-NoReparse $snapshotPath
        $snapshot = Get-Content -LiteralPath $snapshotPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $result.elevated = $snapshot.elevated
        $result.driver_installed = $snapshot.driver_installed
        $result.observed_at = $snapshot.observed_at
    }
    $result | ConvertTo-Json
}

trap {
    $setupFailure = $_
    if ($ElevatedStage -and $administrator) {
        try {
            New-ProtectedDirectory $dataRoot $usersSid
            $errorPath = Join-Path $dataRoot ('setup-error-' + $sid.Value + '.txt')
            Assert-NoReparse $errorPath
            $setupFailure.ToString() | Set-Content -LiteralPath $errorPath -Encoding UTF8
        } catch { }
        Write-Error $setupFailure -ErrorAction Continue
        exit 1
    }
    break
}

if ($Action -eq 'Status') { Show-Status; return }

$files = @()
if ($Action -eq 'Install') {
    if (-not $HelperDirectory) { throw 'Install requires -HelperDirectory with a self-contained sensor publish directory.' }
    $HelperDirectory = (Resolve-Path -LiteralPath $HelperDirectory).Path.TrimEnd('\')
    Assert-NoReparse $HelperDirectory
    foreach ($entry in Get-ChildItem -LiteralPath $HelperDirectory -Recurse -Force) {
        if ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Helper contains a reparse point: $($entry.FullName)" }
        if (-not $entry.PSIsContainer) {
            $files += [pscustomobject]@{ relative = $entry.FullName.Substring($HelperDirectory.Length + 1); sha256 = (Get-FileHash -LiteralPath $entry.FullName -Algorithm SHA256).Hash }
        }
    }
    foreach ($required in @('turzx-sensors.exe', 'turzx-sensors.dll', 'turzx-sensors.runtimeconfig.json', 'coreclr.dll', 'hostfxr.dll', 'LibreHardwareMonitorLib.dll')) {
        if ($required -notin $files.relative) { throw "Self-contained helper is missing $required" }
    }
    $manifest = ($files | Sort-Object relative | ForEach-Object { $_.relative + ':' + $_.sha256 }) -join "`n"
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { $manifestHash = ([BitConverter]::ToString($hasher.ComputeHash([Text.Encoding]::UTF8.GetBytes($manifest)))).Replace('-', '').ToLowerInvariant() }
    finally { $hasher.Dispose() }
    $version = $manifestHash.Substring(0, 20)
    # A fresh protected directory prevents reuse of altered files from an old install.
    $destination = Join-Path $sensorRoot ($version + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
}

# Read-only checks end here; -ValidateOnly reports the hash pin instead of enforcing it.
if ($ValidateOnly) {
    [ordered]@{ action = $Action; task_name = $taskName; source = $HelperDirectory; destination = $destination; snapshot = $snapshotPath; file_count = $files.Count; manifest_hash = $manifestHash; manifest_pinned = ($Action -ne 'Install' -or $manifestHash -eq $expectedManifestHash); elevation_required = -not $administrator } | ConvertTo-Json
    return
}
if ($Action -eq 'Install' -and $manifestHash -ne $expectedManifestHash) { throw 'Helper differs from the pinned, reviewed development publish; refusing installation.' }

if (-not $administrator) {
    if ($ElevatedStage) { throw 'Administrator approval was not granted.' }
    foreach ($arg in @($PSCommandPath, $HelperDirectory)) {
        if ($arg -and $arg.IndexOfAny([char[]]"`"`r`n") -ge 0) { throw 'Unsupported setup path characters.' }
    }
    $arguments = '-NoProfile -File "' + $PSCommandPath + '" -Action ' + $Action + ' -UserSid ' + $sid.Value + ' -ElevatedStage'
    if ($HelperDirectory) { $arguments += ' -HelperDirectory "' + $HelperDirectory + '"' }
    # The current host (powershell.exe 5.1 or pwsh.exe 7) re-runs this script elevated.
    $child = Start-Process -FilePath (Get-Process -Id $PID).Path -Verb RunAs -ArgumentList $arguments -WindowStyle Hidden -Wait -PassThru
    if ($child.ExitCode -ne 0) {
        $details = ''
        $errorPath = Join-Path $dataRoot ('setup-error-' + $sid.Value + '.txt')
        if (Test-Path -LiteralPath $errorPath -PathType Leaf) {
            Assert-ProtectedDirectory $dataRoot
            Assert-NoReparse $errorPath
            $details = Get-Content -LiteralPath $errorPath -Raw -Encoding UTF8
        }
        throw "Elevated sensor setup failed with exit code $($child.ExitCode). $details"
    }
    Show-Status
    return
}

if ($Action -eq 'Remove') {
    $task = Get-OwnedTask
    if ($task) {
        Stop-ScheduledTask -TaskName $taskName -TaskPath '\'
        Unregister-ScheduledTask -TaskName $taskName -TaskPath '\' -Confirm:$false
    }
    # Shared driver and protected binaries are intentionally retained.
    Show-Status
    return
}

# Every executable and output parent is administrator-owned before task registration.
foreach ($dir in @($programRoot, $sensorRoot, $destination)) { New-ProtectedDirectory $dir $sid }
# Shared parents are readable by all users so a second Windows user's daemon passes the parent checks.
foreach ($dir in @($dataRoot, (Join-Path $dataRoot 'Sensors'))) { New-ProtectedDirectory $dir $usersSid }
New-ProtectedDirectory $userData $sid
$existingTask = Get-OwnedTask
if ($existingTask) { throw 'Remove the existing sensor task before installing a different version.' }
foreach ($file in $files) {
    $target = Join-Path $destination $file.relative
    $parent = Split-Path -Parent $target
    Assert-NoReparse $target
    if (-not (Test-Path -LiteralPath $parent)) { [IO.Directory]::CreateDirectory($parent) | Out-Null }
    if (-not (Test-Path -LiteralPath $target)) { Copy-Item -LiteralPath (Join-Path $HelperDirectory $file.relative) -Destination $target }
    if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -ne $file.sha256) { throw "Protected helper hash mismatch: $target" }
}
$executable = Join-Path $destination 'turzx-sensors.exe'
& $executable --self-test | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Protected sensor helper self-test failed.' }
$scheduler = New-Object -ComObject Schedule.Service
$scheduler.Connect()
$definition = $scheduler.NewTask(0)
$definition.RegistrationInfo.Description = 'TURZX read-only hardware sensor snapshots'
$definition.Principal.UserId = $sid.Value
$definition.Principal.LogonType = 3 # TASK_LOGON_INTERACTIVE_TOKEN
$definition.Principal.RunLevel = 1 # TASK_RUNLEVEL_HIGHEST
$trigger = $definition.Triggers.Create(9) # TASK_TRIGGER_LOGON
$trigger.UserId = $sid.Value
$taskAction = $definition.Actions.Create(0) # TASK_ACTION_EXEC
$taskAction.Path = $executable
$taskAction.Arguments = '--snapshot-file "' + $snapshotPath + '"'
$taskAction.WorkingDirectory = $destination
$definition.Settings.Enabled = $false
$definition.Settings.ExecutionTimeLimit = 'PT0S'
$definition.Settings.DisallowStartIfOnBatteries = $false
$definition.Settings.StopIfGoingOnBatteries = $false
$definition.Settings.StartWhenAvailable = $true
$definition.Settings.MultipleInstances = 2 # TASK_INSTANCES_IGNORE_NEW
$definition.Settings.RestartCount = 3
$definition.Settings.RestartInterval = 'PT1M'
$record = [ordered]@{ schema_version = 1; user_sid = $sid.Value; task_name = $taskName; executable = $executable; snapshot = $snapshotPath; files = $files }
Assert-NoReparse $recordPath
$record | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $recordPath -Encoding UTF8
$createdTask = $false
try {
    # Atomically create a disabled task with a protected owner and DACL. 0x12:
    # TASK_CREATE | TASK_DONT_ADD_PRINCIPAL_ACE (not SECURITY_INFORMATION flags).
    $sddl = 'O:BAG:BAD:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGX;;;' + $sid.Value + ')'
    $registered = $scheduler.GetFolder('\').RegisterTaskDefinition($taskName, $definition, 0x12, $sid.Value, $null, 3, $sddl)
    $createdTask = $true
    Get-OwnedTask | Out-Null
    $registered.Enabled = $true
    Start-ScheduledTask -TaskName $taskName -TaskPath '\'
} catch {
    $installError = $_
    if ($createdTask) {
        try {
            $registered.Enabled = $false
            $registered.Stop(0)
            $scheduler.GetFolder('\').DeleteTask($taskName, 0)
            if (Get-ScheduledTask -TaskName $taskName -TaskPath '\' -ErrorAction SilentlyContinue) { throw 'Task remains registered.' }
        } catch {
            throw "Sensor setup failed: $installError. Cleanup also failed: $_. Inspect and remove task '$taskName' from Task Scheduler as administrator."
        }
    }
    throw $installError
}
Show-Status
