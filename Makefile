SHELL := /bin/bash
VERSION ?= 0.1.0-dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: help build test vet fmt check run sidecar webui up down logs clean

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-10s %s\n", $$1, $$2}'

build: ## Build vsd and vs into ./bin
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/vsd ./cmd/vsd
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/vs  ./cmd/vs

test: ## Run the Go test suite
	go test ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the Go sources
	gofmt -w cmd internal

check: vet test ## Vet and test

run: build ## Run the main service in the foreground
	./bin/vsd

sidecar: ## Run the Python sidecar in the foreground
	cd sidecar && python3 -m venv .venv \
	  && .venv/bin/pip install -q -r requirements.txt \
	  && .venv/bin/uvicorn app.main:app --host 127.0.0.1 --port 8090

webui: ## Run the WebUI dev server
	cd webui && npm install && npm run dev

up: ## Start the full stack with Docker Compose
	docker compose up --build -d

down: ## Stop the stack
	docker compose down

logs: ## Follow the stack logs
	docker compose logs -f

clean: ## Remove build output and local runtime data
	rm -rf bin data output
