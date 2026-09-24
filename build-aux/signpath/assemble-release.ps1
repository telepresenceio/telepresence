<#
.SYNOPSIS
Assembles the signed Windows release artifacts into build-output/release.

Repacks the standalone zips with the signed telepresence.exe (files stay
at the zip root) and copies the signed MSIs in alongside them.
#>
param(
    [string]$UnsignedDir = "build-output/unsigned",
    [string]$CoreSignedDir = "build-output/signing/core-signed",
    [string]$ReleaseDir = "build-output/release"
)

$ErrorActionPreference = "Stop"

New-Item -ItemType Directory -Force -Path $ReleaseDir | Out-Null

foreach ($arch in "amd64", "arm64") {
    $expandDir = Join-Path $UnsignedDir $arch
    $signedExe = Join-Path $CoreSignedDir "telepresence-windows-$arch/telepresence.exe"
    $outZip = Join-Path $ReleaseDir "telepresence-windows-$arch.zip"

    Copy-Item $signedExe (Join-Path $expandDir "telepresence.exe") -Force
    if (Test-Path $outZip) {
        Remove-Item $outZip -Force
    }
    Compress-Archive -Path (Join-Path $expandDir "*") -DestinationPath $outZip
}

Copy-Item (Join-Path $CoreSignedDir "telepresence-windows-amd64.msi") (Join-Path $ReleaseDir "telepresence-windows-amd64.msi") -Force
Copy-Item (Join-Path $CoreSignedDir "telepresence-windows-arm64.msi") (Join-Path $ReleaseDir "telepresence-windows-arm64.msi") -Force
