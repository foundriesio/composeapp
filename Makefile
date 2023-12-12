.PHONY: dir

GO ?= go
GOBUILDFLAGS ?=
LDFLAGS ?=
MODVER ?= 1.20
STOREROOT ?=
COMPOSEROOT ?=

bd = bin
exe = composectl
linter = golangci-lint

ifdef STOREROOT
	LDFLAGS += -X 'github.com/foundriesio/composeapp/cmd/composectl/cmd.storeRoot=$(STOREROOT)'
endif
ifdef COMPOSEROOT
    LDFLAGS += -X 'github.com/foundriesio/composeapp/cmd/composectl/cmd.composeRoot=$(COMPOSEROOT)'
endif
ifdef LDFLAGS
	GOBUILDFLAGS += -ldflags="$(LDFLAGS)"
endif

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

check: format
	$(linter) run

tidy-mod:
	go mod tidy -go=$(MODVER)
