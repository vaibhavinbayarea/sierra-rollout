.PHONY: test lint proto

test:
	go test ./...

lint:
	go vet ./...

proto:
	go tool buf generate
