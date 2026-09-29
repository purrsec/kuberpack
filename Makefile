.PHONY: test vet build image

vet:
	go vet ./...

test: vet
	go test ./...

build:
	mkdir -p bin
	go build -o bin/kuberpack ./cmd/kuberpack

image:
	@if command -v docker >/dev/null 2>&1; then docker build -t kuberpack:dev .; \
	elif command -v container >/dev/null 2>&1; then container build -t kuberpack:dev .; \
	else echo "install docker or Apple container CLI" >&2; exit 1; fi
