.PHONY: dir check_connect_timeout deb-image deb deb-lint deb-ci release-prep

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

DEB_IMAGE ?= ghcr.io/foundriesio/debuild-go-min:trixie
DEB_DOCKERFILE ?= debian/Dockerfile
DEB_OUT_DIR ?= $(bd)/deb

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

test-unit:
	@$(GO) test -v ./pkg/compose/... ./internal... ./pkg/docker/...

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

# the followinf targets should be run only in the dev container
preload-images:
	test/fixtures/preload-images.sh

test-e2e: $(exe) preload-images
	@$(GO) test -v ./...

test-smoke: $(exe) preload-images
	@$(GO) test -v -run TestSmoke test/integration/smoke_test.go

deb-image:
	@set -e; \
	if docker image inspect "$(DEB_IMAGE)" >/dev/null 2>&1; then \
		echo "Using local image: $(DEB_IMAGE)"; \
	elif docker pull "$(DEB_IMAGE)"; then \
		echo "Pulled image: $(DEB_IMAGE)"; \
	else \
		echo "Image not found locally or in registry, building: $(DEB_IMAGE)"; \
		docker build -t "$(DEB_IMAGE)" -f "$(DEB_DOCKERFILE)" .; \
	fi

deb: deb-image
	@mkdir -p $(DEB_OUT_DIR)
	docker run --rm -v "$$(pwd)":/src:ro -v "$$(pwd)/$(DEB_OUT_DIR)":/out:rw $(DEB_IMAGE) /src/debian/build-in-docker.sh

deb-lint:
	docker run --rm -u "$$(id -u):$$(id -g)" -v "$$(pwd)/$(DEB_OUT_DIR)":/out:ro $(DEB_IMAGE) bash -lc 'lintian -I /out/*.changes'

deb-ci: deb deb-lint

release-prep: deb-image
	@test -n "$(VERSION)" || (echo "Usage: make $@ VERSION=0.1.1" >&2; exit 2)
	@docker run --rm -it -u "$$(id -u):$$(id -g)" -v "$$PWD":/src:rw -w /src $(DEB_IMAGE) /src/debian/release-prep-changelog-in-docker.sh $(VERSION)
