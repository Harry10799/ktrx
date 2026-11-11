BINARY ?= kubetrace
GO ?= go

.PHONY: build install clean

build:
	$(GO) build -o $(BINARY) ./cmd/kubetrace

install:
	$(GO) install ./cmd/kubetrace

clean:
	rm -f $(BINARY)
