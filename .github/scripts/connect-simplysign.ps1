<#
  connect-simplysign.ps1
  Launch SimplySign Desktop and authenticate with TOTP
  Based on: https://www.devas.life/how-to-automate-signing-your-windows-app-with-certum/
  and https://github.com/browndw/docuscope-ca-desktop/.github/scripts/Connect-SimplySign-Enhanced.ps1
#>

param(
    [string]$OtpUri  = $env:CERTUM_OTP_URI,
    [string]$UserId  = $env:CERTUM_USERNAME,
    [string]$ExePath = $env:CERTUM_EXE_PATH
)

if (-not $OtpUri) { Write-Host "ERROR: CERTUM_OTP_URI not set"; exit 1 }
if (-not $UserId) { Write-Host "ERROR: CERTUM_USERNAME not set"; exit 1 }
if (-not $ExePath) { $ExePath = "C:\Program Files\Certum\SimplySign Desktop\SimplySignDesktop.exe" }

Write-Host "=== SimplySign TOTP Authentication ==="
Write-Host "User: $UserId"
Write-Host "Executable: $ExePath"

if (-not (Test-Path $ExePath)) {
    Write-Host "ERROR: SimplySign Desktop not found at: $ExePath"
    exit 1
}

# === Parse otpauth:// URI ===
$uri = [Uri]$OtpUri

try {
    $q = [System.Web.HttpUtility]::ParseQueryString($uri.Query)
} catch {
    $q = @{}
    foreach ($part in $uri.Query.TrimStart('?') -split '&') {
        $kv = $part -split '=', 2
        if ($kv.Count -eq 2) { $q[$kv[0]] = [Uri]::UnescapeDataString($kv[1]) }
    }
}

$Base32    = $q['secret']
$Digits    = if ($q['digits'])    { [int]$q['digits'] }    else { 6 }
$Period    = if ($q['period'])    { [int]$q['period'] }    else { 30 }
$Algorithm = if ($q['algorithm']) { $q['algorithm'].ToUpper() } else { 'SHA1' }

# === TOTP Generator (inline C#) ===
Add-Type -Language CSharp @"
using System;
using System.Security.Cryptography;

public static class Totp
{
    private const string B32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

    private static byte[] Base32Decode(string s)
    {
        s = s.TrimEnd('=').ToUpperInvariant();
        int byteCount = s.Length * 5 / 8;
        byte[] bytes = new byte[byteCount];
        int bitBuffer = 0, bitsLeft = 0, idx = 0;
        foreach (char c in s)
        {
            int val = B32.IndexOf(c);
            if (val < 0) throw new ArgumentException("Invalid Base32 char: " + c);
            bitBuffer = (bitBuffer << 5) | val;
            bitsLeft += 5;
            if (bitsLeft >= 8)
            {
                bytes[idx++] = (byte)(bitBuffer >> (bitsLeft - 8));
                bitsLeft -= 8;
            }
        }
        return bytes;
    }

    private static HMAC GetHmac(string algo, byte[] key)
    {
        switch (algo)
        {
            case "SHA256": return new HMACSHA256(key);
            case "SHA512": return new HMACSHA512(key);
            default:       return new HMACSHA1(key);
        }
    }

    public static string Now(string secret, int digits, int period, string algorithm)
    {
        byte[] key = Base32Decode(secret);
        long counter = DateTimeOffset.UtcNow.ToUnixTimeSeconds() / period;
        byte[] cnt = BitConverter.GetBytes(counter);
        if (BitConverter.IsLittleEndian) Array.Reverse(cnt);

        byte[] hash;
        using (var hmac = GetHmac(algorithm, key)) { hash = hmac.ComputeHash(cnt); }

        int offset = hash[hash.Length - 1] & 0x0F;
        int binary =
            ((hash[offset]     & 0x7F) << 24) |
            ((hash[offset + 1] & 0xFF) << 16) |
            ((hash[offset + 2] & 0xFF) <<  8) |
             (hash[offset + 3] & 0xFF);

        int otp = binary % (int)Math.Pow(10, digits);
        return otp.ToString(new string('0', digits));
    }
}
"@

$otp = [Totp]::Now($Base32, $Digits, $Period, $Algorithm)
Write-Host "Generated TOTP ($Algorithm): $otp"

# === Launch SimplySign Desktop ===
Write-Host "Launching SimplySign Desktop..."
$proc = Start-Process -FilePath $ExePath -PassThru
Write-Host "Process ID: $($proc.Id)"
Start-Sleep -Seconds 5

$wshell = New-Object -ComObject WScript.Shell

# Focus the window
$focused = $false
for ($i = 0; $i -lt 15; $i++) {
    $focused = $wshell.AppActivate($proc.Id) -or $wshell.AppActivate('SimplySign Desktop')
    if ($focused) { break }
    Start-Sleep -Milliseconds 500
    Write-Host "  Focus attempt $($i + 1)..."
}

if (-not $focused) {
    Write-Host "ERROR: Could not focus SimplySign Desktop window"
    exit 1
}

Write-Host "Window focused"
Start-Sleep -Milliseconds 400

# Inject credentials: username TAB otp ENTER
$wshell.SendKeys($UserId)
Start-Sleep -Milliseconds 200
$wshell.SendKeys("{TAB}")
Start-Sleep -Milliseconds 200
$wshell.SendKeys($otp)
Start-Sleep -Milliseconds 200
$wshell.SendKeys("{ENTER}")

Write-Host "Credentials sent, waiting for authentication..."
Start-Sleep -Seconds 8

# Verify process is still running
if (Get-Process -Id $proc.Id -ErrorAction SilentlyContinue) {
    Write-Host "SUCCESS: SimplySign Desktop is running and authenticated"
} else {
    Write-Host "WARNING: SimplySign Desktop process exited (may indicate auth failure)"
}

Write-Host "=== Authentication complete ==="
