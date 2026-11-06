#!/usr/bin/env bash
# Installs K-Vision on macOS or Linux (Windows: install.ps1). On macOS:
#   /Applications/K-Vision.app   double-click, Spotlight, Dock: opens the window
#   kv on your PATH                   terminal UI, `kv ui`, `kv help`
#
#   ./install.sh             install or update
#   ./install.sh --uninstall remove the app and the kv command (keeps ~/.k-vision: settings, audit log)
set -euo pipefail
cd "$(dirname "$0")"

APP_NAME="K-Vision"
APP_DIR="/Applications/${APP_NAME}.app"
[ -w /Applications ] || APP_DIR="$HOME/Applications/${APP_NAME}.app"
BIN_DIR="/usr/local/bin"
[ -w "$BIN_DIR" ] || BIN_DIR="$HOME/.local/bin"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

if [ "${1:-}" = "--uninstall" ]; then
  pkill -f "${APP_NAME}.app/Contents/MacOS/kv" 2>/dev/null || true
  rm -rf "$APP_DIR" "$HOME/Applications/${APP_NAME}.app"
  # Only remove kv if it is the link this script made.
  for d in /usr/local/bin "$HOME/.local/bin"; do
    [ -L "$d/kv" ] && [[ "$(readlink "$d/kv")" == *"${APP_NAME}.app"* ]] && rm -f "$d/kv"
  done
  rm -f "$HOME/.local/share/applications/k-vision.desktop" "$HOME/.local/share/icons/hicolor/256x256/apps/k-vision.png"
  [ "$(uname -s)" = "Linux" ] && rm -f "$HOME/.local/bin/kv"
  say "Removed. Your settings and audit log are still in ~/.k-vision (rm -rf ~/.k-vision to remove them too)."
  exit 0
fi

if [ "$(uname -s)" = "Linux" ]; then
  BIN_DIR="$HOME/.local/bin"; mkdir -p "$BIN_DIR"
  ICON_DIR="$HOME/.local/share/icons/hicolor/256x256/apps"; mkdir -p "$ICON_DIR"
  if [ -x ./kv ] && [ ! -f go.mod ]; then
    say "Installing kv"; cp ./kv "$BIN_DIR/kv"; [ -f ./k-vision.png ] && cp ./k-vision.png "$ICON_DIR/"
  else
    command -v go >/dev/null || { echo "Go is required to build from source: https://go.dev/dl/"; exit 1; }
    say "Building kv"
    go build -trimpath -ldflags "-s -w" -o "$BIN_DIR/kv" ./cmd/kv
    go run packaging/macos/icon.go "$ICON_DIR/k-vision.png"
  fi
  mkdir -p "$HOME/.local/share/applications"
  cat > "$HOME/.local/share/applications/k-vision.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=K-Vision
Comment=Kubernetes console for every cluster in your kubeconfig
Exec=$BIN_DIR/kv ui
Icon=k-vision
Terminal=false
Categories=Development;
DESKTOP
  case ":$PATH:" in *":$BIN_DIR:"*) ;; *) echo "export PATH=\"$BIN_DIR:\$PATH\"" >> "$HOME/.bashrc"; say "Added $BIN_DIR to PATH in ~/.bashrc";; esac
  say "Done. Terminal: kv   Window: kv ui, or K-Vision in your app menu"
  exit 0
fi

VERSION="$(cat internal/version/VERSION 2>/dev/null || echo 1.0.0)"
BUILD="$(mktemp -d)"
trap 'rm -rf "$BUILD"' EXIT

pkill -f "${APP_NAME}.app/Contents/MacOS/kv" 2>/dev/null || true
if [ -d "./${APP_NAME}.app" ] && [ ! -f go.mod ]; then
  # Unpacked release zip: the app is already built.
  say "Installing ${APP_DIR}"
  rm -rf "$APP_DIR"
  cp -R "./${APP_NAME}.app" "$APP_DIR"
  xattr -dr com.apple.quarantine "$APP_DIR" 2>/dev/null || true
else
  command -v go >/dev/null || { echo "Go is required to build from source: https://go.dev/dl/ (macOS: brew install go)"; exit 1; }
  say "Building kv ${VERSION} (with the native window)"
  MACOSX_DEPLOYMENT_TARGET=12.0 CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0" \
  CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o "$BUILD/kv" ./cmd/kv 2> "$BUILD/build.log" \
    || { cat "$BUILD/build.log"; echo "Build failed. The native window needs the Xcode command line tools: xcode-select --install"; exit 1; }
  say "Installing ${APP_DIR}"
  packaging/macos/bundle.sh "$BUILD/kv" "$BUILD" "$VERSION" >/dev/null
  rm -rf "$APP_DIR"
  cp -R "$BUILD/${APP_NAME}.app" "$APP_DIR"
fi

# The app used to be "Kube Console" with a `kc` command: take the old app and its link
# away so there is one of everything. Settings, audit log and backups move from
# ~/.kube-console to ~/.k-vision by themselves on first start.
if [ "$(uname -s)" = "Darwin" ]; then
  pkill -f "Kube Console.app/Contents/MacOS/kc" 2>/dev/null || true
  for old in "/Applications/Kube Console.app" "$HOME/Applications/Kube Console.app"; do
    [ -d "$old" ] && { rm -rf "$old"; say "Removed the old Kube Console app"; }
  done
fi
for d in /usr/local/bin "$HOME/.local/bin"; do
  [ -L "$d/kc" ] && [[ "$(readlink "$d/kc")" == *"Kube Console.app"* ]] && rm -f "$d/kc"
done
rm -f "$HOME/.local/share/applications/kube-console.desktop"

say "Linking kv into ${BIN_DIR}"
mkdir -p "$BIN_DIR"
ln -sf "$APP_DIR/Contents/MacOS/kv" "$BIN_DIR/kv"
# The old `make install` copy in ~/go/bin would shadow nothing but confuses `which`.
[ -f "$HOME/go/bin/kv" ] && [ ! -L "$HOME/go/bin/kv" ] && rm -f "$HOME/go/bin/kv"

case ":$PATH:" in *":$BIN_DIR:"*) ;; *)
  echo "export PATH=\"$BIN_DIR:\$PATH\"" >> "$HOME/.zshrc"
  say "Added $BIN_DIR to PATH in ~/.zshrc (open a new terminal)";;
esac

say "Done."
cat <<EOF

  Open the app      Spotlight: "K-Vision"  ·  or: open -a "K-Vision"
  Terminal UI       kv
  Window from CLI   kv ui
  Help              kv help
  Uninstall         ./install.sh --uninstall

EOF
