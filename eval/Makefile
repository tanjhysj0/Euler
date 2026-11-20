.PHONY: test lint smoke fmt

test:
	go test ./...

lint:
	golangci-lint run --timeout=5m ./...

fmt:
	gofmt -w .

smoke:
	go run ./cmd/euleval smoke --tasks ./tasks --fixture ./fixture --out ./results/smoke
