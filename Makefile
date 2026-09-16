UI_DIR := web/ui
BIN := bin/runtimeforge

.PHONY: all build ui test vet fmt clean run

all: build

## Build the embedded web UI into web/dist
ui:
	cd $(UI_DIR) && npm install && npm run build

## Build the runtimeforge binary (embedding the current web/dist)
build:
	go build -o $(BIN) ./cmd/runtimeforge

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run: build
	$(BIN) serve

clean:
	rm -rf $(BIN)
