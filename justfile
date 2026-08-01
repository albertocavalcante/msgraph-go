# msgraph-go development commands

default: test

build:
    go build ./...

test:
    go tool -modfile=tools.go.mod gotestsum --format pkgname-and-test-fails -- ./...

test-race:
    go tool -modfile=tools.go.mod gotestsum --format pkgname-and-test-fails -- -race ./...

test-cover:
    go tool -modfile=tools.go.mod gotestsum --format pkgname-and-test-fails -- -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out -o coverage.html

lint:
    go tool -modfile=tools.go.mod golangci-lint run --timeout=5m

fmt:
    gofmt -w .

fmt-check:
    test -z "$(gofmt -l .)"

vet:
    go vet ./...

tidy:
    go mod tidy

tidy-tools:
    go mod tidy -modfile=tools.go.mod

ci: fmt-check vet test-race lint

clean:
    rm -rf bin coverage.out coverage.html
