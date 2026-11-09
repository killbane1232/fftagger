.PHONY: build app package run test check bench clean
GO ?= go
export GOCACHE := $(CURDIR)/.cache/go-build
export MACOSX_DEPLOYMENT_TARGET := 13.0
CGO_CFLAGS ?= -O2 -g
CGO_LDFLAGS ?= -O2 -g
export CGO_CFLAGS := $(CGO_CFLAGS) -mmacosx-version-min=13.0
export CGO_LDFLAGS := $(CGO_LDFLAGS) -mmacosx-version-min=13.0
build:
	mkdir -p build
	$(GO) build -trimpath -o build/fftagger ./cmd/fftagger
app: build
	mkdir -p build/FFTagger.app/Contents/MacOS build/FFTagger.app/Contents/Resources
	cp build/fftagger build/FFTagger.app/Contents/MacOS/fftagger
	cp packaging/Info.plist build/FFTagger.app/Contents/Info.plist
	codesign --force --deep --sign - build/FFTagger.app
package: app
	ditto -c -k --sequesterRsrc --keepParent build/FFTagger.app build/FFTagger-macOS.zip
run:
	$(GO) run ./cmd/fftagger
test:
	$(GO) test ./...
check:
	$(GO) vet ./...
	$(GO) test -race ./...
bench:
	$(GO) test ./internal/library -run '^$$' -bench BenchmarkScanWorkers -benchtime=1x -count=1
clean:
	rm -rf build
