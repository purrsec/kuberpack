.PHONY: test test-static test-heavy test-superheavy vet build image image-builder

vet:
	go vet ./...

test:
	python3 test/run.py light

test-static:
	python3 test/run.py static

test-heavy:
	python3 test/run.py heavy

test-superheavy:
	python3 test/run.py superheavy

build:
	mkdir -p bin
	for name in kuberpack kuberpack-admin kuberpack-control-plane kuberpack-builder kuberpack-reloader; do go build -o bin/$$name ./cmd/$$name || exit; done

image:
	@if command -v docker >/dev/null 2>&1; then docker build -t kuberpack:dev .; \
	elif command -v container >/dev/null 2>&1; then container build -t kuberpack:dev .; \
	else echo "install docker or Apple container CLI" >&2; exit 1; fi

image-builder:
	@if command -v docker >/dev/null 2>&1; then docker build --platform linux/amd64 -f Dockerfile.builder -t kuberpack-builder:dev .; \
	elif command -v container >/dev/null 2>&1; then container build --platform linux/amd64 -f Dockerfile.builder -t kuberpack-builder:dev .; \
	else echo "install docker or Apple container CLI" >&2; exit 1; fi
