BUILD_DIR := build
CXX       := clang++

-include ge/Module.mk
ge/Module.mk:
	git submodule update --init --recursive

# ── Flags ────────────────────────────────────────────
CXXFLAGS   := -std=c++20 -O2 -Wall $(ge/INCLUDES)
SDL_CFLAGS :=
SDL_LIBS   := $(ge/SDL_LIBS)
FRAMEWORKS := -framework Metal -framework QuartzCore -framework Foundation \
              -framework CoreFoundation -framework IOKit -framework IOSurface \
              -framework CoreGraphics -framework CoreServices \
              -framework AudioToolbox -framework AVFoundation -framework CoreMedia \
              -framework CoreVideo -framework GameController -framework CoreHaptics \
              -framework CoreMotion -framework ImageIO

# ── C++ app ──────────────────────────────────────────
SRC := src/main.cpp src/App.cpp
OBJ := $(patsubst %.cpp,$(BUILD_DIR)/%.o,$(SRC))
APP := bin/jevons

COMPILE_DB_DEPS += $(SRC) Makefile

# ── Default target ───────────────────────────────────
.PHONY: all
all: $(APP) jevonsd remote

# ── C++ binary ───────────────────────────────────────
$(APP): $(OBJ) $(ge/SESSION_WIRE_OBJ) $(ge/LIB) $(ge/FRAMEWORK_LIBS)
	@mkdir -p $(@D)
	$(CXX) $(OBJ) $(ge/SESSION_WIRE_OBJ) $(ge/LIB) $(ge/DAWN_LIBS) \
		$(FRAMEWORKS) $(SDL_LIBS) -o $@

$(BUILD_DIR)/src/%.o: src/%.cpp
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) $(SDL_CFLAGS) -MMD -MP -c $< -o $@

-include $(OBJ:.o=.d)

# ── Player ───────────────────────────────────────────
.PHONY: player
player: $(ge/PLAYER)

# ── Go binaries ─────────────────────────────────────
VERSION  ?= dev
LDFLAGS  := -ldflags "-X github.com/marcelocantos/jevons/internal/cli.Version=$(VERSION)"
# mattn/go-sqlite3 needs these defines for sqlpipe session extension support.
export CGO_CFLAGS += -DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK -DSQLITE_ENABLE_FTS5
# Suppress macOS linker warning about duplicate -lm from go-sqlite3 opt files.
export CGO_LDFLAGS += -Wl,-no_warn_duplicate_libraries
GO_TAGS  := -tags "sqlite_preupdate_hook sqlite_fts5"
GO_SRC   := $(shell find cmd internal -name '*.go' 2>/dev/null)
EMBED_GUIDE := internal/cli/help_agent.md

$(EMBED_GUIDE): agents-guide.md
	cp $< $@

.PHONY: jevonsd
jevonsd: bin/jevonsd

bin/jevonsd: $(GO_SRC) $(EMBED_GUIDE)
	@mkdir -p bin
	go build $(GO_TAGS) $(LDFLAGS) -o bin/jevonsd ./cmd/jevonsd

.PHONY: remote
remote: bin/remote

bin/remote: $(GO_SRC) $(EMBED_GUIDE)
	@mkdir -p bin
	go build $(GO_TAGS) $(LDFLAGS) -o bin/remote ./cmd/remote

# ── Run ──────────────────────────────────────────────
.PHONY: run run-app run-jevonsd run-remote
run-app: $(APP)
	$(APP)

run-jevonsd: bin/jevonsd
	bin/jevonsd

run-remote: bin/remote
	bin/remote

run: $(APP) bin/jevonsd
	@trap 'kill 0' INT TERM; \
	bin/jevonsd & \
	$(APP) & \
	wait

# ── Setup ────────────────────────────────────────────
.PHONY: init
init: ge/init
	@echo "── jevons project setup ──"
	@command -v go >/dev/null 2>&1 || { echo "ERROR: Go not found. Install from https://go.dev/dl/"; exit 1; }
	@echo "  Go: $$(go version)"
	@go mod download
	@echo "  Go dependencies downloaded"
	$(ge/INIT_DONE)

# ── iOS app ─────────────────────────────────────────
.PHONY: ios
ios:
	cd ios && xcodegen generate

# ── Test ─────────────────────────────────────────────
.PHONY: test test-go
test-go:
	go test ./...

test: test-go

# ── Standing invariants (bullseye) ──────────────────
.PHONY: bullseye
bullseye:
	@go build $(GO_TAGS) ./... && echo "✓ build"
	@go test $(GO_TAGS) ./... && echo "✓ tests"
	@go vet $(GO_TAGS) ./... && echo "✓ vet"
	@test -z "$$(git status --porcelain)" && echo "✓ clean" || \
	 (echo "✗ dirty tree"; git status --short; exit 1)
