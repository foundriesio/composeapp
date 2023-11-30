.PHONY: dir

GO ?= go
GOBUILDFLAGS ?=

bd = bin
exe = composectl
linter = golangci-lint

all: $(exe) 

format:
	@$(GO) fmt ./...

test:
	@$(GO) test ./...

$(bd):
	@mkdir -p $@

$(exe): $(bd)
	$(GO) build $(GOBUILDFLAGS) -o $(bd)/$@ cmd/composectl/main.go

clean:
	@rm -r $(bd)

check:
	@test -z $(shell go fmt ./..) || echo "[WARN] fix formatting issues with 'make format'"
	$(linter) run

