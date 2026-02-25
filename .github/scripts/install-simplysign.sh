#!/bin/bash
# Install SimplySign Desktop silently on Windows
# Adapted from https://github.com/browndw/docuscope-ca-desktop

set -e

INSTALLER_URL="https://www.files.certum.eu/software/SimplySignDesktop/Windows/9.4.0.84/SimplySignDesktop-9.4.0.84-64-bit-en.msi"
MSI_FILE="SimplySignDesktop.msi"
INSTALL_DIR="C:\\Program Files\\Certum\\SimplySign Desktop"

echo "=== Installing SimplySign Desktop ==="

# Download installer
echo "Downloading SimplySign Desktop..."
curl -L -o "$MSI_FILE" --connect-timeout 30 --max-time 300 "$INSTALLER_URL"

if [ ! -f "$MSI_FILE" ]; then
    echo "ERROR: Failed to download installer"
    exit 1
fi

echo "Downloaded: $(wc -c < "$MSI_FILE") bytes"

# Install silently
echo "Installing SimplySign Desktop..."
MSI_WIN_PATH=$(cygpath -w "$(pwd)/$MSI_FILE")
powershell -Command "Start-Process msiexec -ArgumentList '/i','\"$MSI_WIN_PATH\"','/quiet','/norestart','ALLUSERS=1','/l*v','install.log' -Wait -NoNewWindow" &
INSTALL_PID=$!

# Monitor installation (up to 3 minutes)
TIMEOUT=180
ELAPSED=0
while kill -0 $INSTALL_PID 2>/dev/null; do
    if [ $ELAPSED -ge $TIMEOUT ]; then
        echo "ERROR: Installation timed out after ${TIMEOUT}s"
        exit 1
    fi
    sleep 10
    ELAPSED=$((ELAPSED + 10))
    echo "  Waiting... (${ELAPSED}s)"
done

wait $INSTALL_PID || true

# Verify installation
if [ -d "$INSTALL_DIR" ]; then
    echo "SUCCESS: SimplySign Desktop installed at $INSTALL_DIR"
    ls -la "$INSTALL_DIR"/ | head -10
else
    echo "ERROR: Installation directory not found"
    echo "Install log (last 20 lines):"
    tail -20 install.log 2>/dev/null || echo "(no log)"
    exit 1
fi

# Clean up
rm -f "$MSI_FILE"
echo "=== SimplySign Desktop installation complete ==="
