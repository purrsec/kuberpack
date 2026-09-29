.PHONY: test vet build

vet:
	go vet ./...

test: vet
	go test ./...

build:
	mkdir -p bin
	go build -o bin/platform-deployer ./cmd/platform-deployer
