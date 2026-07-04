GOLANGCI_LINT_VERSION ?= v2.12.2

.PHONY: all check build test vet lint fmt tidy tools clean

all: check

## check: build, vet, lint and test everything
check: build vet lint test

## build: compile all packages and the CLI
build:
	go build ./...

## test: run tests with the race detector
test:
	go test -race ./...

## vet: run go vet
vet:
	go vet ./...

## lint: run golangci-lint (needs `make tools`)
lint:
	golangci-lint run

## fmt: format the code (gofmt + goimports via golangci-lint)
fmt:
	golangci-lint fmt

## tidy: tidy go.mod/go.sum
tidy:
	go mod tidy

## tools: install the pinned golangci-lint
tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

## clean: remove build artifacts
clean:
	rm -f blissctl
