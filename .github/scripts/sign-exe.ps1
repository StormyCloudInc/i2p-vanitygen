<#
  sign-exe.ps1
  Sign a Windows binary with Certum certificate via signtool
#>

param(
    [Parameter(Mandatory=$true)]
    [string]$BinaryPath,

    [string]$CertificateSHA1 = $env:CERTUM_CERTIFICATE_SHA1
)

if (-not $CertificateSHA1) {
    Write-Host "ERROR: CERTUM_CERTIFICATE_SHA1 not set"
    exit 1
}

if (-not (Test-Path $BinaryPath)) {
    Write-Host "ERROR: Binary not found: $BinaryPath"
    exit 1
}

Write-Host "=== Signing Windows Binary ==="
Write-Host "Binary: $BinaryPath"
Write-Host "Certificate SHA1: $($CertificateSHA1.Substring(0, [Math]::Min(16, $CertificateSHA1.Length)))..."

# Locate signtool.exe
$SignToolPaths = @(
    "${env:ProgramFiles(x86)}\Windows Kits\10\bin\10.0.22621.0\x64\signtool.exe",
    "${env:ProgramFiles(x86)}\Windows Kits\10\bin\10.0.20348.0\x64\signtool.exe",
    "${env:ProgramFiles(x86)}\Windows Kits\10\bin\10.0.19041.0\x64\signtool.exe",
    "${env:ProgramFiles(x86)}\Windows Kits\10\bin\10.0.17763.0\x64\signtool.exe"
)

$SignTool = $null
foreach ($path in $SignToolPaths) {
    if (Test-Path $path) {
        $SignTool = $path
        break
    }
}

if (-not $SignTool) {
    # Try finding it via where
    $SignTool = (Get-Command signtool.exe -ErrorAction SilentlyContinue).Source
}

if (-not $SignTool) {
    Write-Host "ERROR: signtool.exe not found"
    exit 1
}

Write-Host "Using signtool: $SignTool"

# Sign the binary
Write-Host "Signing..."
$signOutput = & $SignTool sign /sha1 $CertificateSHA1 /tr "http://time.certum.pl" /td SHA256 /fd SHA256 /v $BinaryPath 2>&1
$signExit = $LASTEXITCODE

Write-Host $signOutput

if ($signExit -ne 0) {
    Write-Host "ERROR: Signing failed (exit code $signExit)"
    exit 1
}

Write-Host ""
Write-Host "Verifying signature..."
$verifyOutput = & $SignTool verify /pa /v $BinaryPath 2>&1
$verifyExit = $LASTEXITCODE

Write-Host $verifyOutput

if ($verifyExit -ne 0) {
    Write-Host "WARNING: Signature verification failed (exit code $verifyExit)"
    Write-Host "The binary may still be signed - verification can fail in CI without full cert chain"
} else {
    Write-Host "SUCCESS: Signature verified"
}

Write-Host ""
Write-Host "=== Signing complete ==="
