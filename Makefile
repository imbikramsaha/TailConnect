VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test test-short vet macos clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tailconnect ./cmd/tailconnect

run: build
	./dist/tailconnect

test:
	go test -race -count=1 ./...

test-short:
	go test -short ./...

vet:
	go vet ./...
	GOOS=windows go vet ./...
	GOOS=linux go vet ./...

macos:
	scripts/build-macos.sh $(VERSION)

clean:
	rm -rf dist
