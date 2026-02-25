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
Start-Sleep -Seconds 10

# === Post-authentication diagnostics ===
Write-Host ""
Write-Host "=== Post-Auth Diagnostics ==="

# List all visible windows
Write-Host ""
Write-Host "Visible windows:"
Add-Type @"
using System;
using System.Runtime.InteropServices;
using System.Text;
using System.Collections.Generic;

public class WinEnum {
    [DllImport("user32.dll")] static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);
    [DllImport("user32.dll")] static extern int GetWindowText(IntPtr hWnd, StringBuilder lpString, int nMaxCount);
    [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr hWnd);
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint lpdwProcessId);

    public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
    public static List<string> Windows = new List<string>();

    public static void Enumerate() {
        Windows.Clear();
        EnumWindows((hWnd, lParam) => {
            if (IsWindowVisible(hWnd)) {
                StringBuilder sb = new StringBuilder(256);
                GetWindowText(hWnd, sb, 256);
                string title = sb.ToString();
                if (!string.IsNullOrWhiteSpace(title)) {
                    uint pid;
                    GetWindowThreadProcessId(hWnd, out pid);
                    Windows.Add(string.Format("[PID {0}] {1}", pid, title));
                }
            }
            return true;
        }, IntPtr.Zero);
    }
}
"@
[WinEnum]::Enumerate()
foreach ($w in [WinEnum]::Windows) {
    Write-Host "  $w"
}

# Take a screenshot for debugging
Write-Host ""
Write-Host "Capturing screenshot..."
try {
    Add-Type -AssemblyName System.Windows.Forms
    $screen = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
    $bitmap = New-Object System.Drawing.Bitmap($screen.Width, $screen.Height)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $graphics.CopyFromScreen($screen.Location, [System.Drawing.Point]::Empty, $screen.Size)
    $screenshotPath = "$env:GITHUB_WORKSPACE\simplysign-screenshot.png"
    $bitmap.Save($screenshotPath)
    $graphics.Dispose()
    $bitmap.Dispose()
    Write-Host "Screenshot saved to: $screenshotPath"
} catch {
    Write-Host "Screenshot failed: $_"
}

# Check certificate stores
Write-Host ""
Write-Host "Checking certificate stores..."
foreach ($storeName in @("My", "SmartCardRoot", "Root")) {
    foreach ($location in @("CurrentUser", "LocalMachine")) {
        try {
            $store = New-Object System.Security.Cryptography.X509Certificates.X509Store($storeName, $location)
            $store.Open("ReadOnly")
            $certs = $store.Certificates
            if ($certs.Count -gt 0) {
                Write-Host "  ${location}\${storeName}: $($certs.Count) cert(s)"
                foreach ($cert in $certs) {
                    Write-Host "    $($cert.Thumbprint) | $($cert.Subject)"
                }
            }
            $store.Close()
        } catch {}
    }
}

# Check smart card subsystem
Write-Host ""
Write-Host "Smart card info (certutil -scinfo):"
$scinfo = certutil -scinfo 2>&1
Write-Host ($scinfo | Out-String)

# Check CSP providers
Write-Host ""
Write-Host "Registered CSP providers:"
$cspOutput = certutil -csplist 2>&1
foreach ($line in $cspOutput) {
    if ($line -match "SimplySign|SmartSign|Certum|proCertum") {
        Write-Host "  >>> $line"
    } elseif ($line -match "Provider Name:") {
        Write-Host "  $line"
    }
}

# Verify process is still running
Write-Host ""
if (Get-Process -Id $proc.Id -ErrorAction SilentlyContinue) {
    Write-Host "SimplySign Desktop is running (PID: $($proc.Id))"
} else {
    Write-Host "WARNING: SimplySign Desktop process exited"
}

Write-Host "=== Authentication complete ==="
