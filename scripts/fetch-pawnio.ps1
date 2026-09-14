# SPDX-License-Identifier: GPL-3.0-or-later
# Fetch and verify the PawnIO setup executable; never install it.
[CmdletBinding()]
param(
    [string]$Path = (Join-Path (Split-Path -Parent $PSScriptRoot) '.tools/pawnio/2.2.0/PawnIO_setup.exe'),
    [switch]$VerifyOnly
)

$ErrorActionPreference = 'Stop'
$sourceUrl = 'https://github.com/namazso/PawnIO.Setup/releases/download/2.2.0/PawnIO_setup.exe'
$expectedSha256 = '1f519a22e47187f70a1379a48ca604981c4fcf694f4e65b734aaa74a9fba3032'
# Full signer subject of the pinned 2.2.0 release (thumbprint F380DCC9F706E2756A5047B832FFE719E1BC35F5).
$expectedSignerSubject = 'E=admin@namazso.eu, CN=namazso.eu, O=namazso, L=Debrecen, C=HU'

function Test-PawnIOFile([string]$FilePath) {
    if (-not (Test-Path -LiteralPath $FilePath -PathType Leaf)) {
        throw "PawnIO setup file does not exist: $FilePath"
    }
    $hash = (Get-FileHash -LiteralPath $FilePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($hash -ne $expectedSha256) {
        throw "PawnIO SHA-256 mismatch for $FilePath (got $hash)"
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $FilePath
    if ($signature.Status -ne 'Valid') {
        throw "PawnIO Authenticode signature is not valid for $FilePath ($($signature.Status))"
    }
    $subject = [string]$signature.SignerCertificate.Subject
    if ($subject -cne $expectedSignerSubject) {
        throw "Unexpected PawnIO signer subject: $subject"
    }
    [pscustomobject]@{ Path = [System.IO.Path]::GetFullPath($FilePath); Sha256 = $hash; SignerSubject = $subject }
}

$Path = [System.IO.Path]::GetFullPath($Path)
# Output is the verified object: Path, Sha256, SignerSubject.
if ($VerifyOnly) { Test-PawnIOFile $Path; return }

if (Test-Path -LiteralPath $Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "PawnIO output path is not a file: $Path" }
    Test-PawnIOFile $Path
    return
}

$directory = Split-Path -Parent $Path
New-Item -ItemType Directory -Path $directory -Force | Out-Null
$temporaryPath = Join-Path $directory ('.' + [System.IO.Path]::GetRandomFileName() + '.download')
try {
    Invoke-WebRequest -Uri $sourceUrl -OutFile $temporaryPath -UseBasicParsing
    Test-PawnIOFile $temporaryPath | Out-Null
    if (Test-Path -LiteralPath $Path) { throw "PawnIO output appeared during download: $Path" }
    Move-Item -LiteralPath $temporaryPath -Destination $Path
    Test-PawnIOFile $Path
}
finally {
    if (Test-Path -LiteralPath $temporaryPath) { Remove-Item -LiteralPath $temporaryPath -Force }
}
