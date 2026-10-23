GO ?= go
GOFMT ?= gofmt
NODE ?= node
NPM ?= npm
BROWSER_PATH := $(shell dirname "$$(command -v "$(NODE)")")
GO_SOURCES := $(shell git ls-files --cached --others --exclude-standard '*.go')

.PHONY: build build-signed run fmt fmt-check vet test check check-android check-m2 check-channels check-browser check-m6 check-package-scripts browser-install clean

build:
	/bin/sh tools/build-release.sh "$(GO)"

build-signed:
	PUBLISHER_REQUIRE_SIGNING=1 $(MAKE) build

package-macos:
	/bin/sh tools/package-macos.sh "$(GO)"

run:
	$(GO) run ./cmd/release --help

fmt:
	$(GOFMT) -w $(GO_SOURCES)

fmt-check:
	@unformatted="$$($(GOFMT) -l $(GO_SOURCES))" || exit $$?; \
	if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi

vet:
	$(GO) vet ./...

test: check-browser
	PUBLISHER_REQUIRE_BROWSER_TESTS=1 PUBLISHER_NODE="$(shell command -v "$(NODE)")" $(GO) test -race -timeout=120s ./...

browser-install:
	PATH="$(BROWSER_PATH):$$PATH" $(NPM) --prefix browser ci --ignore-scripts
	PATH="$(BROWSER_PATH):$$PATH" $(NPM) --prefix browser exec -- playwright install chromium

check-browser:
	$(NODE) -e 'if (process.version !== "v" + require("fs").readFileSync(".node-version", "utf8").trim()) throw Error("Use .node-version runtime")'
	PATH="$(BROWSER_PATH):$$PATH" $(NPM) --prefix browser run check

check-m6: check-browser
	PUBLISHER_REQUIRE_BROWSER_TESTS=1 PUBLISHER_NODE="$(shell command -v "$(NODE)")" $(GO) test -race -timeout=120s ./internal/browser ./internal/simulation ./internal/state ./internal/engine ./internal/cli -count=1

check-android:
	PUBLISHER_REQUIRE_ANDROID_TESTS=1 $(GO) test -race -timeout=120s ./internal/artifact -run TestAndroidSignatureIntegration -count=1

check-m2:
	$(GO) test -race -timeout=120s ./internal/account ./internal/credentials ./internal/transport ./internal/cli ./internal/state -count=1

check-channels:
	$(GO) test -race -timeout=120s ./internal/channels ./internal/engine ./internal/state ./internal/transport -count=1

check-package-scripts:
	/bin/sh tools/check-package-scripts.sh

check: fmt-check vet test check-package-scripts build

clean:
	rm -rf bin
