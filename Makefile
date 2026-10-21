.PHONY: test lint proto build integration e2e dev-temporal

test:
	go test ./...

lint:
	go vet ./...

proto:
	go run github.com/bufbuild/buf/cmd/buf@v1.47.2 generate

build:
	go build -o bin/ ./cmd/...

integration:
	go test -tags integration -count=1 -timeout 15m ./test/integration/...

e2e:
	./test/integration/e2e.sh

dev-temporal:
	temporal server start-dev
