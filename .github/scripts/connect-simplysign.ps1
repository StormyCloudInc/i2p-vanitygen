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

function Get-TotpRemainingSeconds {
    param([int]$TotpPeriod)
    $epoch = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
    return ($TotpPeriod - ($epoch % $TotpPeriod))
}

function New-ReadyTotp {
    param([int]$MinRemainingSeconds = 8)

    $remaining = Get-TotpRemainingSeconds -TotpPeriod $Period
    if ($remaining -lt $MinRemainingSeconds) {
        $sleepFor = $remaining + 1
        Write-Host "TOTP window too close to expiry (${remaining}s left), waiting ${sleepFor}s for next period..."
        Start-Sleep -Seconds $sleepFor
    }

    $code = [Totp]::Now($Base32, $Digits, $Period, $Algorithm)
    $remaining = Get-TotpRemainingSeconds -TotpPeriod $Period
    return @{
        Code = $code
        Remaining = $remaining
    }
}

# === Launch SimplySign Desktop ===
Write-Host "Launching SimplySign Desktop..."
$proc = Start-Process -FilePath $ExePath -PassThru
Write-Host "Process ID: $($proc.Id)"
Start-Sleep -Seconds 5

# Diagnostic: log username info (without revealing full value)
Write-Host "Username length: $($UserId.Length), starts with: $($UserId.Substring(0, [Math]::Min(3, $UserId.Length)))..."

# === Use UI Automation to inject credentials directly into fields ===
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type -AssemblyName System.Windows.Forms

Write-Host ""
Write-Host "Finding SimplySign window via UI Automation..."

$root = [System.Windows.Automation.AutomationElement]::RootElement

# Find the SimplySign window by process ID
$pidCondition = New-Object System.Windows.Automation.PropertyCondition(
    [System.Windows.Automation.AutomationElement]::ProcessIdProperty, $proc.Id)

$ssWindow = $null
for ($i = 0; $i -lt 20; $i++) {
    $ssWindow = $root.FindFirst([System.Windows.Automation.TreeScope]::Children, $pidCondition)
    if ($ssWindow) { break }
    Start-Sleep -Milliseconds 500
    Write-Host "  Waiting for window... ($($i + 1))"
}

if (-not $ssWindow) {
    Write-Host "ERROR: Could not find SimplySign window via UI Automation"
    exit 1
}

Write-Host "Found window: $($ssWindow.Current.Name)"

$allCondition = [System.Windows.Automation.Condition]::TrueCondition

# === Step 1: Dismiss update dialog if present ===
Write-Host ""
Write-Host "Checking for update dialog..."
$allElements = $ssWindow.FindAll([System.Windows.Automation.TreeScope]::Descendants, $allCondition)

# Look for the "New version" update prompt
$updateDialog = $null
$noButton = $null
foreach ($el in $allElements) {
    $name = $el.Current.Name
    if ($name -match 'New version.*found') {
        $updateDialog = $el
        Write-Host "  Found update dialog: '$name'"
    }
    # The No button has AutomationId='7' in the #32770 dialog
    if ($el.Current.AutomationId -eq '7' -and $el.Current.Name -eq 'No') {
        $noButton = $el
    }
}

if ($updateDialog -and $noButton) {
    Write-Host "Dismissing update dialog by clicking 'No'..."
    try {
        $noButton.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke()
        Write-Host "  Clicked 'No' via InvokePattern"
    } catch {
        Write-Host "  InvokePattern failed, trying SetFocus + SendKeys..."
        $noButton.SetFocus()
        Start-Sleep -Milliseconds 200
        [System.Windows.Forms.SendKeys]::SendWait("{ENTER}")
    }
    Start-Sleep -Seconds 2
    Write-Host "Update dialog dismissed"
} elseif ($updateDialog) {
    Write-Host "  Update dialog found but No button not found, pressing Escape..."
    $wshell = New-Object -ComObject WScript.Shell
    $wshell.AppActivate($proc.Id)
    Start-Sleep -Milliseconds 300
    $wshell.SendKeys("{ESCAPE}")
    Start-Sleep -Seconds 2
} else {
    Write-Host "  No update dialog found"
}

# === Step 2: Defer TOTP generation until right before token field submission ===
Write-Host ""
Write-Host "Will generate TOTP immediately before token entry..."

# === Step 3: Find login form fields (WinForms controls show as Pane, not Edit) ===
Write-Host ""
Write-Host "Finding login form controls..."

# Re-enumerate after dismissing update dialog
$allElements = $ssWindow.FindAll([System.Windows.Automation.TreeScope]::Descendants, $allCondition)

# Find EDIT fields by WinForms class name (they appear as ControlType.Pane)
$idField = $null
$tokenField = $null
$okButton = $null

foreach ($el in $allElements) {
    $cls = $el.Current.ClassName
    $aid = $el.Current.AutomationId
    $name = $el.Current.Name

    # The ID (email) field: AutomationId='262190', Class contains 'EDIT'
    if ($cls -match 'EDIT' -and $aid -eq '262190') {
        $idField = $el
        Write-Host "  Found ID field (AutomationId=$aid)"
    }
    # The Token field: AutomationId='131642', Class contains 'EDIT'
    if ($cls -match 'EDIT' -and $aid -eq '131642') {
        $tokenField = $el
        Write-Host "  Found Token field (AutomationId=$aid)"
    }
    # The Ok button: AutomationId='131526', Class contains 'BUTTON'
    if ($cls -match 'BUTTON' -and $aid -eq '131526') {
        $okButton = $el
        Write-Host "  Found Ok button (AutomationId=$aid)"
    }
}

# Fallback: find EDIT fields by class name pattern if specific IDs not found
if (-not $idField -or -not $tokenField) {
    Write-Host "  Specific IDs not found, searching by class pattern..."
    $editFields = @()
    foreach ($el in $allElements) {
        if ($el.Current.ClassName -match 'EDIT') {
            $editFields += $el
            Write-Host "  Found EDIT-class control: AutomationId='$($el.Current.AutomationId)'"
        }
    }
    if ($editFields.Count -ge 2 -and -not $idField) { $idField = $editFields[1] }
    if ($editFields.Count -ge 1 -and -not $tokenField) { $tokenField = $editFields[0] }
}

if (-not $okButton) {
    foreach ($el in $allElements) {
        if ($el.Current.Name -eq 'Ok' -and $el.Current.ClassName -match 'BUTTON') {
            $okButton = $el
        }
    }
}

# === Step 4: Fill in credentials ===
if ($idField -and $tokenField) {
    Write-Host ""
    Write-Host "Setting ID (email)..."
    try {
        $idField.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern).SetValue($UserId)
        Write-Host "  Set via ValuePattern"
    } catch {
        Write-Host "  ValuePattern failed ($($_.Exception.Message)), using SetFocus + SendKeys"
        $idField.SetFocus()
        Start-Sleep -Milliseconds 300
        [System.Windows.Forms.SendKeys]::SendWait("^a")
        Start-Sleep -Milliseconds 100
        [System.Windows.Forms.SendKeys]::SendWait($UserId)
    }

    Write-Host "Setting Token..."
    $otpData = New-ReadyTotp -MinRemainingSeconds 8
    $otp = $otpData.Code
    Write-Host "Fresh TOTP ($Algorithm): $otp (valid for $($otpData.Remaining)s)"
    try {
        $tokenField.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern).SetValue($otp)
        Write-Host "  Set via ValuePattern"
    } catch {
        Write-Host "  ValuePattern failed ($($_.Exception.Message)), using SetFocus + SendKeys"
        $tokenField.SetFocus()
        Start-Sleep -Milliseconds 300
        [System.Windows.Forms.SendKeys]::SendWait($otp)
    }

    Write-Host "Clicking Ok..."
    if ($okButton) {
        try {
            $okButton.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke()
            Write-Host "  Clicked via InvokePattern"
        } catch {
            Write-Host "  InvokePattern failed, using SetFocus + Enter"
            $okButton.SetFocus()
            Start-Sleep -Milliseconds 200
            [System.Windows.Forms.SendKeys]::SendWait("{ENTER}")
        }
    } else {
        Write-Host "  Ok button not found, pressing Enter in token field..."
        $tokenField.SetFocus()
        Start-Sleep -Milliseconds 200
        [System.Windows.Forms.SendKeys]::SendWait("{ENTER}")
    }
} else {
    Write-Host "ERROR: Could not find login form fields!"
    Write-Host "  ID field: $($idField -ne $null)"
    Write-Host "  Token field: $($tokenField -ne $null)"
    Write-Host "Dumping all elements:"
    foreach ($el in $allElements) {
        Write-Host "  Class='$($el.Current.ClassName)' | Name='$($el.Current.Name)' | AID='$($el.Current.AutomationId)'"
    }
    exit 1
}

Write-Host ""
Write-Host "Credentials injected, waiting for authentication..."
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
