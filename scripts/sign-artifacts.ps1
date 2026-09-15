# SPDX-License-Identifier: GPL-3.0-or-later
# Authenticode-sign the executables we build and ship.
#
# A self-signed certificate gives file integrity, a stable publisher identity
# and a tamper check, and it is what -CreateSelfSigned produces. It is NOT
# publicly trusted: SmartScreen and antivirus heuristics treat such a binary
# like an unsigned one unless the certificate is installed in the Trusted Root
# and Trusted Publishers stores of every machine that runs it. Swapping in a
# publicly trusted certificate later only changes which -Thumbprint is passed.
[CmdletBinding()]
param(
    [string]$PayloadDirectory = 'bin/app',
    # The sensor helper publish directory, when its executable should be signed
    # in the same pass. Signing changes the files, so the pinned manifest hash
    # in setup-sensor-task.ps1 must be recomputed afterwards.
    [string]$HelperDirectory,
    # The build-ffmpeg.sh output directory. Sign ffmpeg.exe there rather than in
    # the payload, then re-run build-ffmpeg.ps1 -FromBuildOutput: that re-proves
    # the signed binary against the real pipeline and copies it into the
    # payload, keeping payload and build record identical.
    [string]$FFmpegBuildOutput,
    # Certificate to sign with: a thumbprint in the current user's store, or a
    # PFX file. Ignored when -CreateSelfSigned is given without either.
    [string]$Thumbprint,
    [string]$PfxPath,
    [securestring]$PfxPassword,
    # Timestamping keeps signatures valid after the certificate expires. A
    # self-signed certificate can still be timestamped; pass an empty string to
    # skip it when offline.
    [string]$TimestampServer = 'http://timestamp.digicert.com',
    [switch]$CreateSelfSigned,
    [string]$Subject = 'CN=TURZX Control (self-signed development build)',
    [int]$ValidYears = 3
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot

function Join-ProjectPath([string]$Path) {
    if ([IO.Path]::IsPathRooted($Path)) { return $Path }
    return Join-Path $projectRoot $Path
}

if ($CreateSelfSigned) {
    if ($Thumbprint -or $PfxPath) { throw 'Pass -CreateSelfSigned on its own, then sign with the thumbprint it prints.' }
    $created = New-SelfSignedCertificate -Type CodeSigningCert -Subject $Subject `
        -CertStoreLocation 'Cert:\CurrentUser\My' -KeyUsage DigitalSignature `
        -NotAfter (Get-Date).AddYears($ValidYears)
    [pscustomobject]@{
        Subject = $created.Subject
        Thumbprint = $created.Thumbprint
        NotAfter = $created.NotAfter
        Store = 'Cert:\CurrentUser\My'
        Note = 'Self-signed: not publicly trusted. Sign with -Thumbprint <this>.'
    } | Format-List
    return
}

if ($Thumbprint -and $PfxPath) { throw 'Pass either -Thumbprint or -PfxPath, not both.' }
if (-not $Thumbprint -and -not $PfxPath) { throw 'Pass -Thumbprint or -PfxPath, or -CreateSelfSigned to make one first.' }

if ($PfxPath) {
    $PfxPath = Join-ProjectPath $PfxPath
    if (-not (Test-Path -LiteralPath $PfxPath -PathType Leaf)) { throw "PFX not found: $PfxPath" }
    if (-not $PfxPassword) { throw '-PfxPassword is required with -PfxPath.' }
    $certificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new(
        $PfxPath, $PfxPassword, 'Exportable,PersistKeySet')
}
else {
    $certificate = Get-ChildItem 'Cert:\CurrentUser\My' -CodeSigningCert |
        Where-Object { $_.Thumbprint -eq $Thumbprint } | Select-Object -First 1
    if (-not $certificate) { throw "No code signing certificate with thumbprint $Thumbprint in Cert:\CurrentUser\My" }
}
if (-not $certificate.HasPrivateKey) { throw 'The certificate has no private key; it cannot sign.' }

# Only what we build. Third-party binaries keep whatever provenance they came
# with; signing someone else's file with our certificate would misattribute it.
$ours = @('turzx-control.exe', 'turzx-claude-status.exe')
if (-not $FFmpegBuildOutput) { $ours += 'ffmpeg.exe' }
$targets = @()
$PayloadDirectory = Join-ProjectPath $PayloadDirectory
if (-not (Test-Path -LiteralPath $PayloadDirectory -PathType Container)) { throw "Payload directory not found: $PayloadDirectory" }
foreach ($name in $ours) {
    $path = Join-Path $PayloadDirectory $name
    if (Test-Path -LiteralPath $path -PathType Leaf) { $targets += $path }
}
if ($FFmpegBuildOutput) {
    $FFmpegBuildOutput = Join-ProjectPath $FFmpegBuildOutput
    $ffmpeg = Join-Path $FFmpegBuildOutput 'ffmpeg.exe'
    if (-not (Test-Path -LiteralPath $ffmpeg -PathType Leaf)) { throw "FFmpeg build output not found: $ffmpeg" }
    $targets += $ffmpeg
}
if ($HelperDirectory) {
    $HelperDirectory = Join-ProjectPath $HelperDirectory
    $helper = Join-Path $HelperDirectory 'turzx-sensors.exe'
    if (-not (Test-Path -LiteralPath $helper -PathType Leaf)) { throw "Sensor helper not found: $helper" }
    $targets += $helper
}
if (-not $targets) { throw "Nothing to sign in $PayloadDirectory" }

$results = foreach ($target in $targets) {
    $arguments = @{ FilePath = $target; Certificate = $certificate; HashAlgorithm = 'SHA256' }
    if ($TimestampServer) { $arguments.TimestampServer = $TimestampServer }
    $signed = Set-AuthenticodeSignature @arguments
    if ($signed.Status -ne 'Valid' -and $signed.Status -ne 'UnknownError') {
        throw "Signing failed for ${target}: $($signed.Status) $($signed.StatusMessage)"
    }
    $check = Get-AuthenticodeSignature -LiteralPath $target
    [pscustomobject]@{
        File = Split-Path -Leaf $target
        Signer = $check.SignerCertificate.Subject
        # Valid means the chain is trusted on THIS machine. A self-signed
        # certificate that is not in the trust store reports UnknownError even
        # though the signature itself is intact.
        Status = $check.Status
        Timestamped = [bool]$check.TimeStamperCertificate
    }
}
$results | Format-Table -AutoSize

if ($FFmpegBuildOutput) {
    Write-Output ''
    Write-Output 'Signed ffmpeg.exe in the build output. Re-run build-ffmpeg.ps1 -FromBuildOutput on'
    Write-Output 'that directory to re-verify it and copy it into the payload before packaging.'
}
if ($HelperDirectory) {
    Write-Output ''
    Write-Output 'Signed the sensor helper. Recompute the pinned manifest hash in setup-sensor-task.ps1'
    Write-Output '(-Action Install -ValidateOnly prints it) or installation will be refused.'
}

$untrusted = $results | Where-Object { $_.Status -ne 'Valid' }
if ($untrusted) {
    Write-Output ''
    Write-Output 'Signatures are in place but not trusted on this machine. That is expected for a'
    Write-Output 'self-signed certificate. To make Windows accept it here, an administrator installs'
    Write-Output 'the certificate into Trusted Root and Trusted Publishers; on unmanaged machines it'
    Write-Output 'stays untrusted and SmartScreen still warns.'
}
