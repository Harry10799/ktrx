BINARY ?= ktrx
GO ?= go

.PHONY: build install clean

build:
	$(GO) build -o $(BINARY) ./cmd/ktrx

install:
	$(GO) install ./cmd/ktrx

clean:
	rm -f $(BINARY)
