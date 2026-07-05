GOLANGCI_LINT_VERSION ?= v2.12.2
BUMP ?= patch

.PHONY: all check build test vet lint fmt tidy tools clean bump

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
	rm -f blissctl blissha

## bump: tag the next semver release (BUMP=patch|minor|major); does not push
bump:
	@test -z "$$(git status --porcelain)" || { echo "working tree not clean; commit or stash first"; exit 1; }
	@latest=$$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0); \
	ver=$${latest#v}; \
	major=$$(echo $$ver | cut -d. -f1); minor=$$(echo $$ver | cut -d. -f2); patch=$$(echo $$ver | cut -d. -f3); \
	case "$(BUMP)" in \
	  major) major=$$((major+1)); minor=0; patch=0;; \
	  minor) minor=$$((minor+1)); patch=0;; \
	  patch) patch=$$((patch+1));; \
	  *) echo "BUMP must be patch, minor or major (got '$(BUMP)')"; exit 1;; \
	esac; \
	next=v$$major.$$minor.$$patch; \
	echo "bumping $$latest -> $$next"; \
	git tag -a $$next -m "Release $$next"; \
	echo "tagged $$next — publish with: git push origin main $$next"
