BINARY ?= wetransfer

.PHONY: build test vet install clean

build:
	go build -o $(BINARY) ./cmd/wetransfer

test:
	go test ./...

vet:
	go vet ./...

install:
	go install ./cmd/wetransfer

clean:
	go clean
