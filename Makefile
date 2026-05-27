.PHONY: build test install clean fmt vet

BIN_DIR := $(HOME)/bin
BIN     := styx

build:
	go build -o ./bin/$(BIN) ./cmd/styx

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

install: build
	@if [ -f $(BIN_DIR)/$(BIN) ] && [ ! -L $(BIN_DIR)/$(BIN) ]; then \
		mv $(BIN_DIR)/$(BIN) $(BIN_DIR)/$(BIN).old.bak; \
		echo "Backed up existing $(BIN) to $(BIN_DIR)/$(BIN).old.bak"; \
	fi
	mkdir -p $(BIN_DIR)
	cp ./bin/$(BIN) $(BIN_DIR)/$(BIN)
	@echo "Installed to $(BIN_DIR)/$(BIN)"

clean:
	rm -rf ./bin
