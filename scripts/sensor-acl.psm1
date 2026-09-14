# SPDX-License-Identifier: GPL-3.0-or-later
# Protected-directory ACL helpers shared by setup-sensor-task.ps1 and test-sensor-task.ps1.
$ErrorActionPreference = 'Stop'
$adminSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')
$systemSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
$usersSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-545')

function Assert-NoReparse([string]$Path) {
    $itemPath = [IO.Path]::GetFullPath($Path)
    while ($itemPath) {
        if (Test-Path -LiteralPath $itemPath) {
            $item = Get-Item -LiteralPath $itemPath -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Reparse point is not permitted: $itemPath" }
        }
        $parent = [IO.Path]::GetDirectoryName($itemPath)
        if ($parent -eq $itemPath) { break }
        $itemPath = $parent
    }
}

# Owner must be Administrators/SYSTEM with a protected DACL. Non-administrator
# principals (the installing user, BUILTIN\Users on shared parents) may hold
# read-only rights only; any write/delete/DACL/owner right is rejected.
function Assert-ProtectedDirectory([string]$Path) {
    Assert-NoReparse $Path
    $acl = Get-Acl -LiteralPath $Path
    $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($owner -notin @($adminSid.Value, $systemSid.Value) -or -not $acl.AreAccessRulesProtected) {
        throw "Existing directory is not owned and protected by administrators: $Path"
    }
    $readOnly = [Security.AccessControl.FileSystemRights]::ReadAndExecute -bor [Security.AccessControl.FileSystemRights]::Synchronize
    foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
        if ($rule.AccessControlType -eq 'Allow' -and $rule.IdentityReference.Value -notin @($adminSid.Value, $systemSid.Value)) {
            if (($rule.FileSystemRights -band (-bnot [int]$readOnly)) -ne 0) { throw "Non-administrator write access on $Path" }
        }
    }
}

# $ReaderSid gets ReadAndExecute: the installing user's SID for per-user
# directories, BUILTIN\Users for the shared %ProgramData% parents so another
# Windows user's daemon can pass the parent-directory checks.
function New-ProtectedDirectory([string]$Path, [Security.Principal.SecurityIdentifier]$ReaderSid) {
    Assert-NoReparse $Path
    if (Test-Path -LiteralPath $Path) { Assert-ProtectedDirectory $Path; return }
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner($adminSid)
    $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    foreach ($trustedSid in @($adminSid, $systemSid)) {
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($trustedSid, 'FullControl', $inherit, 'None', 'Allow'))
    }
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($ReaderSid, 'ReadAndExecute', $inherit, 'None', 'Allow'))
    [IO.Directory]::CreateDirectory($Path, $acl) | Out-Null
    Assert-ProtectedDirectory $Path
}

Export-ModuleMember -Function Assert-NoReparse, Assert-ProtectedDirectory, New-ProtectedDirectory -Variable adminSid, systemSid, usersSid
