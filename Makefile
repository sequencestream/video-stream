SHELL := /bin/bash
VERSION ?= 0.1.0-dev
LDFLAGS := -X main.version=$(VERSION)
PREFIX ?= /usr/local

.PHONY: help build install uninstall test vet fmt secrets check whisper docker clean

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-10s %s\n", $$1, $$2}'

build: ## Build vs into ./bin
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/vs ./cmd/vs

install: build ## Install vs into $(PREFIX)/bin
	install -m 0755 bin/vs $(PREFIX)/bin/vs
	@echo "installed $(PREFIX)/bin/vs"
	@$(PREFIX)/bin/vs doctor || true

uninstall: ## Remove the installed binary
	rm -f $(PREFIX)/bin/vs

test: ## Run the Go test suite
	go test ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the Go sources
	gofmt -w cmd internal

secrets: ## Fail if a plaintext credential was committed
	./scripts/check-secrets.sh

check: vet test secrets ## Vet, test and scan for committed credentials

whisper: ## Install the speech recognition backend for the current interpreter
	python3 -m pip install --upgrade faster-whisper

docker: ## Build a container with vs, ffmpeg and faster-whisper
	docker build -t video-stream/vs:$(VERSION) .

clean: ## Remove build output
	rm -rf bin dist
