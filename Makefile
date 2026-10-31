BINARY  ?= wetransfer
VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || echo 0.3.0)
LDFLAGS := -X wetransfer/internal/version.Label=$(VERSION)

.PHONY: build test vet install dist clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/wetransfer

test:
	go test ./...

vet:
	go vet ./...

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/wetransfer

dist:
	mkdir -p dist
	rm -f dist/wetransfer_* dist/SHA256SUMS
	for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "-s -w $(LDFLAGS)" -o $(BINARY) ./cmd/wetransfer; \
		tar -czf dist/wetransfer_$${os}_$${arch}.tar.gz $(BINARY); \
	done
	rm -f $(BINARY)
	cd dist && (command -v sha256sum >/dev/null 2>&1 && sha256sum * > SHA256SUMS || shasum -a 256 * > SHA256SUMS)

clean:
	go clean
	rm -f $(BINARY)
