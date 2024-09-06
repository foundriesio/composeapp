.PHONY: dir check_connect_timeout

GO ?= go
GOBUILDFLAGS ?=
LDFLAGS ?=
MODVER ?= 1.20
DOCKERCFGDIR ?=
STOREROOT ?=
COMPOSEROOT ?=
CONNECTTIMEOUT ?=
BASESYSTEMCONFIG ?=

bd = bin
exe = composectl
linter = golangci-lint

commit = $(shell git rev-parse HEAD)

ifneq ($(strip $(commit)),)
	LDFLAGS += -X 'github.com/foundriesio/composeapp/cmd/composectl/cmd.commit=$(commit)'
endif

ifdef DOCKERCFGDIR
    LDFLAGS += -X 'github.com/foundriesio/composeapp/cmd/composectl/cmd.overrideConfigDir=$(DOCKERCFGDIR)'
endif
ifdef STOREROOT
	LDFLAGS += -X 'github.com/foundriesio/composeapp/cmd/composectl/cmd.storeRoot=$(STOREROOT)'
endif
ifdef COMPOSEROOT
    LDFLAGS += -X 'github.com/foundriesio/composeapp/cmd/composectl/cmd.composeRoot=$(COMPOSEROOT)'
endif
ifdef CONNECTTIMEOUT
    LDFLAGS += -X 'github.com/foundriesio/composeapp/cmd/composectl/cmd.defConnectTimeout=$(CONNECTTIMEOUT)'
endif
ifdef BASESYSTEMCONFIG
    LDFLAGS += -X 'github.com/foundriesio/composeapp/cmd/composectl/cmd.baseSystemConfig=$(BASESYSTEMCONFIG)'
endif

ifdef LDFLAGS
	GOBUILDFLAGS += -ldflags="$(LDFLAGS)"
endif

all: $(exe) 

check_connect_timeout:
	@if [ -n "$(strip $(CONNECTTIMEOUT))" ] && ! [ "$(strip $(CONNECTTIMEOUT))" -eq "$(strip $(CONNECTTIMEOUT))" ] 2>/dev/null; then \
		echo "ERR: invalid CONNECTTIMEOUT value ($(CONNECTTIMEOUT)); it must be integer."; \
		exit 1; \
    fi

format:
	@$(GO) fmt ./...

test:
	@$(GO) test ./...

$(bd):
	@mkdir -p $@

$(exe): $(bd) check_connect_timeout
	$(GO) build -tags publish $(GOBUILDFLAGS) -o $(bd)/$@ cmd/composectl/main.go

clean:
	@rm -r $(bd)

check: format
	$(linter) run

tidy-mod:
	go mod tidy -go=$(MODVER)
