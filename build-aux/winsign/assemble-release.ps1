<#
.SYNOPSIS
Assembles the signed Windows release zip for one architecture into
build-output/release.

.DESCRIPTION
Copies the signed telepresence.exe over the unsigned one and repacks the
zip (files stay at the zip root). The signed MSI is already in the
release directory.
#>
param(
    [Parameter(Mandatory)][string]$Arch,
    [string]$UnsignedDir = "build-output/unsigned",
    [string]$SignedBinDir = "build-output/bin",
    [string]$ReleaseDir = "build-output/release"
)

$ErrorActionPreference = "Stop"

New-Item -ItemType Directory -Force -Path $ReleaseDir | Out-Null

$expandDir = Join-Path $UnsignedDir $Arch
$signedExe = Join-Path $SignedBinDir "telepresence.exe"
$outZip = Join-Path $ReleaseDir "telepresence-windows-$Arch.zip"

Copy-Item $signedExe (Join-Path $expandDir "telepresence.exe") -Force
if (Test-Path $outZip) {
    Remove-Item $outZip -Force
}
Compress-Archive -Path (Join-Path $expandDir "*") -DestinationPath $outZip
