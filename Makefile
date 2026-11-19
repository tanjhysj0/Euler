.PHONY: build test lint fmt tidy web-install web-test web-build test-e2e dev-api dev-web

build:
	npm --prefix web ci && npm --prefix web run build
	go build -o bin/euler ./cmd/euler

test:
	go test ./...

lint:
	golangci-lint run --timeout=5m ./...

fmt:
	gofmt -w ./cmd ./internal

tidy:
	go mod tidy

web-install:
	npm install --prefix web

web-test:
	npm --prefix web test -- --run

web-build:
	npm --prefix web run build

test-e2e:
	npm --prefix web run test:e2e

dev-api:
	go run ./cmd/euler

dev-web:
	npm --prefix web run dev
