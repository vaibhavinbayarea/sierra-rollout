.PHONY: test lint

test:
	go test ./...

lint:
	go vet ./...
