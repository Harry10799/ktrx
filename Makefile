BINARY ?= ktrx
GO ?= go

.PHONY: build install clean fmt fmt-check test vet

build:
	$(GO) build -o $(BINARY) ./cmd/ktrx

install:
	$(GO) install ./cmd/ktrx

fmt:
	gofmt -w $$(find . -type f -name '*.go' -not -path './vendor/*')

fmt-check:
	@test -z "$$($(GO)fmt -l .)" || (echo "Go files need gofmt; run make fmt" && $(GO)fmt -l . && exit 1)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY)
