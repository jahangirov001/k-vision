#!/usr/bin/env bash
# Builds "K-Vision.app" around a kv binary:
#   packaging/macos/bundle.sh <kv binary> <output dir> <version>
# Used by install.sh (this Mac's arch) and `make dist` (universal arm64+x86_64).
set -euo pipefail
KC="$1"; OUT="$2"; VERSION="$3"
APP_NAME="K-Vision"
APP="$OUT/${APP_NAME}.app"
cd "$(dirname "$0")/../.."
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

go run packaging/macos/icon.go "$WORK/icon.png"
ICONSET="$WORK/AppIcon.iconset"; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -z $s $s "$WORK/icon.png" --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  sips -z $((s*2)) $((s*2)) "$WORK/icon.png" --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$WORK/AppIcon.icns"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$KC" "$APP/Contents/MacOS/kv"
cp "$WORK/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"

# Finder starts apps with a bare PATH, so kubeconfig's `aws eks get-token` (or
# gke-gcloud-auth-plugin, kubelogin...) would not be found. The launcher asks the
# login shell for its PATH first.
cat > "$APP/Contents/MacOS/launch" <<'LAUNCH'
#!/bin/bash
SHELL_PATH="$("${SHELL:-/bin/zsh}" -lic 'printf %s "$PATH"' 2>/dev/null | tail -n1)"
export PATH="${SHELL_PATH:-$PATH}:/usr/local/bin:/opt/homebrew/bin"
mkdir -p "$HOME/.k-vision"
KV_IN_APP=1 exec "$(dirname "$0")/kv" ui >>"$HOME/.k-vision/ui.log" 2>&1
LAUNCH
chmod +x "$APP/Contents/MacOS/launch"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>${APP_NAME}</string>
  <key>CFBundleDisplayName</key><string>${APP_NAME}</string>
  <key>CFBundleIdentifier</key><string>local.k-vision</string>
  <key>CFBundleExecutable</key><string>launch</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSAppTransportSecurity</key><dict>
    <!-- The UI is served by kv itself on 127.0.0.1; nothing else is loaded. -->
    <key>NSAllowsLocalNetworking</key><true/>
  </dict>
</dict></plist>
PLIST
# Ad-hoc signature. Without an Apple Developer ID the app is not notarized: on another
# Mac the first open needs right-click → Open (or the quarantine flag removed).
codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || true
echo "$APP"
