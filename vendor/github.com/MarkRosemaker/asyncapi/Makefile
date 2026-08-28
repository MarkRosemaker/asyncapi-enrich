.PHONY: test fix

fix:
	gofumpt -l -w --extra .
	go fix ./...
	golangci-lint run --fix

test:
	go test -race ./...
