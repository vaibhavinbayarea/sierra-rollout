.PHONY: test lint proto build integration e2e dev-temporal

test:
	go test ./...

lint:
	go vet ./...

proto:
	go tool buf generate

build:
	go build -o bin/ ./cmd/...

integration:
	go test -tags integration -count=1 -timeout 15m ./test/integration/...

e2e:
	./test/integration/e2e.sh

dev-temporal:
	temporal server start-dev
