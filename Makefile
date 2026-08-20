.PHONY: help
help:  ## Show this help menu
	@echo "Usage: make [TARGET ...]"
	@echo ""
	@grep --no-filename -E '^[a-zA-Z_%-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "% -25s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the Go application
	go build -o antibot ./src

.PHONY: run
run: ## Run the Go application (requires GH_PAT)
	go run ./src -config config.json

.PHONY: test
test: ## Run the Go tests
	go test -v -race ./src

.PHONY: tidy
tidy: ## Tidy the Go module dependencies
	go mod tidy

.PHONY: fmt
fmt: ## Format Go source files
	go fmt ./src