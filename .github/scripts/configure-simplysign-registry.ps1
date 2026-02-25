<#
  configure-simplysign-registry.ps1
  Configure SimplySign Desktop registry for automated login
#>

$RegPath = "HKCU:\Software\Certum\SimplySign"

# Settings that enable auto-login dialog for automation
$Settings = @{
    "ShowLoginDialogOnStart"      = 1
    "ShowLoginDialogOnAppRequest" = 1
    "RememberLastUsername"         = 1
    "RememberPINInCSP"            = 0
    "UnregisterCertsOnDisconnect" = 1
    "LangID"                      = 9  # English
}

Write-Host "=== Configuring SimplySign Desktop Registry ==="

# Create registry path if it doesn't exist
if (-not (Test-Path $RegPath)) {
    New-Item -Path $RegPath -Force | Out-Null
    Write-Host "Created registry path: $RegPath"
}

# Apply settings
foreach ($key in $Settings.Keys) {
    $value = $Settings[$key]
    Set-ItemProperty -Path $RegPath -Name $key -Value $value -Type DWord -Force
    Write-Host "  Set $key = $value"
}

# Verify
Write-Host ""
Write-Host "Verifying configuration..."
$allGood = $true
foreach ($key in $Settings.Keys) {
    $actual = (Get-ItemProperty -Path $RegPath -Name $key -ErrorAction SilentlyContinue).$key
    if ($actual -ne $Settings[$key]) {
        Write-Host "  MISMATCH: $key = $actual (expected $($Settings[$key]))"
        $allGood = $false
    }
}

if ($allGood) {
    Write-Host "SUCCESS: All registry settings configured"
} else {
    Write-Host "ERROR: Some settings did not apply correctly"
    exit 1
}

Write-Host "=== Registry configuration complete ==="
