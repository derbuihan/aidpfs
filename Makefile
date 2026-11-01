.PHONY: fmt test build
fmt:
	gofmt -w cmd internal
test:
	go test ./...
build:
	go build ./cmd/aidpfs
