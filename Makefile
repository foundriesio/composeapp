.PHONY: dir

GO ?= go
GOBUILDFLAGS ?=
MODVER ?= 1.20

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

tidy-mod:
	go mod tidy -go=$(MODVER)
