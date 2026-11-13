VERSION := $(shell cat internal/version/VERSION)
BIN      = kv
GOBIN   ?= $(shell go env GOPATH)/bin
APP      = $(HOME)/Applications/K-Vision.app

.PHONY: build test install app uninstall release dist clean

build:
	go build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/kv

test:
	go vet ./...
	go test ./...
	node --check internal/web/static/app.js
	node --check internal/web/static/filter.js
	node internal/web/jstest/filter_test.js

install: build
	mkdir -p $(GOBIN)
	cp $(BIN) $(GOBIN)/$(BIN)
	@echo "installed $(GOBIN)/$(BIN)"

# The macOS app in /Applications plus `kv` on your PATH. See install.sh.
app:
	./install.sh

uninstall:
	./install.sh --uninstall

# kv for every platform, into dist/. Copy the Windows .exe next to install.ps1.
PLATFORMS = darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64
release:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "  dist/kv_$${os}_$${arch}$$ext"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/kv_$${os}_$${arch}$$ext ./cmd/kv || exit 1; \
	done
	@cp install.ps1 dist/

# What to hand to other people, in dist/:
#   K-Vision-<v>-macos.zip / .dmg   universal app (Apple silicon + Intel) + install.sh
#   K-Vision-<v>-linux-<arch>.tar.gz kv + install.sh + icon
#   K-Vision-<v>-windows-<arch>.zip kv.exe + install.ps1
#   SHA256SUMS
# The macOS app is ad-hoc signed, not notarized (that needs an Apple Developer ID):
# people open it the first time with right-click → Open, or install.sh does it for them.
dist: test
	@rm -rf dist && mkdir -p dist/stage
	@echo "  macOS universal app"
	@MACOSX_DEPLOYMENT_TARGET=12.0 CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0" CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o dist/stage/kv-arm64 ./cmd/kv
	@MACOSX_DEPLOYMENT_TARGET=12.0 CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0" CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/stage/kv-amd64 ./cmd/kv
	@lipo -create -output dist/stage/kv dist/stage/kv-arm64 dist/stage/kv-amd64
	@mkdir -p "dist/stage/K-Vision-$(VERSION)-macos"
	@packaging/macos/bundle.sh dist/stage/kv "dist/stage/K-Vision-$(VERSION)-macos" $(VERSION) >/dev/null
	@cp install.sh "dist/stage/K-Vision-$(VERSION)-macos/"
	@cd dist/stage && ditto -c -k --keepParent "K-Vision-$(VERSION)-macos" "../K-Vision-$(VERSION)-macos.zip"
	@ln -s /Applications "dist/stage/K-Vision-$(VERSION)-macos/Applications"
	@hdiutil create -quiet -volname "K-Vision $(VERSION)" -srcfolder "dist/stage/K-Vision-$(VERSION)-macos" -ov -format UDZO "dist/K-Vision-$(VERSION)-macos.dmg"
	@go run packaging/macos/icon.go dist/stage/k-vision.png
	@for p in linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
	  os=$${p%/*}; arch=$${p#*/}; d="dist/stage/K-Vision-$(VERSION)-$$os-$$arch"; mkdir -p "$$d"; \
	  echo "  $$os/$$arch"; \
	  if [ $$os = windows ]; then \
	    GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$$d/kv.exe" ./cmd/kv || exit 1; \
	    cp install.ps1 "$$d/"; (cd dist/stage && zip -qr "../K-Vision-$(VERSION)-$$os-$$arch.zip" "K-Vision-$(VERSION)-$$os-$$arch"); \
	  else \
	    GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$$d/kv" ./cmd/kv || exit 1; \
	    cp install.sh dist/stage/k-vision.png "$$d/"; tar -C dist/stage -czf "dist/K-Vision-$(VERSION)-$$os-$$arch.tar.gz" "K-Vision-$(VERSION)-$$os-$$arch"; \
	  fi; \
	done
	@rm -rf dist/stage
	@cd dist && shasum -a 256 K-Vision-* > SHA256SUMS
	@ls -lh dist

clean:
	rm -rf $(BIN) dist
