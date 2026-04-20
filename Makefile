BINARY_NAME=proxyma
GO=go
LINTER=golangci-lint
TEMP_DIR=/tmp/proxyma-dev

# Colores para la terminal
BLUE=\033[0;34m
NC=\033[0m # No Color

.PHONY: all build test lint clean help init-cluster issue-cert

all: lint test build

build:
	@echo "$(BLUE)Compiling Proxyma...$(NC)"
	$(GO) build -o $(BINARY_NAME) .

test:
	@echo "$(BLUE)Running tests...$(NC)"
	$(GO) test -v ./...

lint:
	@echo "$(BLUE)Running golangci-lint...$(NC)"
	$(LINTER) run

clean:
	@echo "$(BLUE)Cleaning...$(NC)"
	rm -f $(BINARY_NAME)
	rm -rf $(TEMP_DIR)

init-cluster: build
	@echo "$(BLUE)Generating master cluster identity...$(NC)"
	mkdir -p $(TEMP_DIR)
	./$(BINARY_NAME) init -path $(TEMP_DIR)

issue-cert: build
	@ifndef ID
		$(error ID is not defined. Usage: make issue-cert ID=my-device)
	endif
	@echo "$(BLUE)Issuing certificate for node $(ID)...$(NC)"
	./$(BINARY_NAME) issue -id $(ID) -ca $(TEMP_DIR) -node-path $(TEMP_DIR)

help:
	@echo "Available commands:"
	@sed -n 's/^##//p' $< | column -t -s ':' |  sed -e 's/^/ /'
