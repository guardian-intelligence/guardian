CARGO ?= cargo
PREFIX ?= /usr/local
DESTDIR ?=

BIN = pipe-to-remote-box
RELEASE = target/release/$(BIN)
BINDIR = $(DESTDIR)$(PREFIX)/bin

.PHONY: all install uninstall clean check

all:
	$(CARGO) build --release --locked

# Building as root would use root's Cargo home. Build first, then let this
# target install only the reviewed release binary.
install:
	install -d "$(BINDIR)"
	install -m 0755 "$(RELEASE)" "$(BINDIR)/$(BIN)"

uninstall:
	rm -f "$(BINDIR)/$(BIN)"
	@echo "removed $(BINDIR)/$(BIN)"

clean:
	$(CARGO) clean

check:
	$(CARGO) test --locked
