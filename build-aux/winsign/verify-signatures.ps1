<#
.SYNOPSIS
Verifies that every release artifact in a directory carries a valid,
timestamped Authenticode signature.

.DESCRIPTION
Checks every top-level .exe and .msi directly, checks the telepresence.exe
inside every .zip, and performs an administrative install of every .msi to
check the .exe files packaged inside it (this reaches the deep-signed
telepresence.exe and TelepresenceDaemon.exe). Exits 1 if any checked file is
missing a valid, timestamped signature, or if the directory holds no
artifacts at all.

.PARAMETER Dir
Directory to scan. Defaults to build-output/release.
#>

param(
    [string]$Dir = "build-output/release"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:Failed = $false
$script:CheckedAny = $false
$script:TempDirs = @()

function New-ScratchDir {
    $path = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString())
    New-Item -ItemType Directory -Path $path | Out-Null
    $script:TempDirs += $path
    return $path
}

function Test-Signature {
    param(
        [Parameter(Mandatory)][string]$Path,
        [string]$Label = $Path
    )

    $script:CheckedAny = $true
    $sig = Get-AuthenticodeSignature -FilePath $Path

    $valid = $sig.Status -eq 'Valid'
    $timestamped = $null -ne $sig.TimeStamperCertificate

    if ($valid -and $timestamped) {
        $signer = $sig.SignerCertificate.Subject
        $tsa = $sig.TimeStamperCertificate.Subject
        Write-Host "OK   $Label -- signer: $signer, timestamped by: $tsa"
    }
    else {
        $script:Failed = $true
        $reason = if (-not $valid) { "status=$($sig.Status)" } else { "no timestamp" }
        Write-Host "FAIL $Label -- $reason"
    }
}

function Test-Zip {
    param([Parameter(Mandatory)][string]$ZipPath)

    $extractDir = New-ScratchDir
    Expand-Archive -Path $ZipPath -DestinationPath $extractDir -Force

    $exe = Get-ChildItem -Path $extractDir -Filter 'telepresence.exe' -Recurse | Select-Object -First 1
    if (-not $exe) {
        $script:Failed = $true
        Write-Host "FAIL $ZipPath -- telepresence.exe not found inside zip"
        return
    }
    Test-Signature -Path $exe.FullName -Label "$ZipPath (telepresence.exe)"
}

function Test-Msi {
    param([Parameter(Mandatory)][string]$MsiPath)

    Test-Signature -Path $MsiPath

    $installDir = New-ScratchDir
    $msiArg = "/a `"$MsiPath`" /qn TARGETDIR=`"$installDir`""
    $proc = Start-Process -FilePath 'msiexec' -ArgumentList $msiArg -Wait -PassThru -NoNewWindow
    if ($proc.ExitCode -ne 0) {
        $script:Failed = $true
        Write-Host "FAIL $MsiPath -- administrative install failed (exit $($proc.ExitCode))"
        return
    }

    $exes = Get-ChildItem -Path $installDir -Filter '*.exe' -Recurse
    if (-not $exes) {
        $script:Failed = $true
        Write-Host "FAIL $MsiPath -- no .exe files found in administrative install"
        return
    }
    foreach ($exe in $exes) {
        Test-Signature -Path $exe.FullName -Label "$MsiPath ($($exe.Name))"
    }
}

try {
    if (-not (Test-Path -Path $Dir)) {
        throw "directory not found: $Dir"
    }

    Get-ChildItem -Path $Dir -Filter '*.exe' -File | ForEach-Object { Test-Signature -Path $_.FullName }
    Get-ChildItem -Path $Dir -Filter '*.msi' -File | ForEach-Object { Test-Msi -MsiPath $_.FullName }
    Get-ChildItem -Path $Dir -Filter '*.zip' -File | ForEach-Object { Test-Zip -ZipPath $_.FullName }

    if (-not $script:CheckedAny) {
        Write-Host "FAIL no artifacts found in $Dir"
        $script:Failed = $true
    }
}
finally {
    foreach ($t in $script:TempDirs) {
        Remove-Item -Path $t -Recurse -Force -ErrorAction SilentlyContinue
    }
}

if ($script:Failed) {
    exit 1
}
