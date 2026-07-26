CARGO ?= cargo
PREFIX ?= /usr/local
DESTDIR ?=

BIN = postflight
RELEASE = target/release/$(BIN)
BINDIR = $(DESTDIR)$(PREFIX)/bin

.PHONY: all install uninstall clean check

all:
	$(CARGO) build --release --locked

# install deliberately does not depend on all: the documented flow is
# `make && sudo make install`, and building under sudo would run cargo as
# root against root's CARGO_HOME.
install:
	install -d $(BINDIR)
	install -m 0755 $(RELEASE) $(BINDIR)/$(BIN)

# `postflight self uninstall` declines a copy installed this way — there is no
# receipt saying the file is its to remove — so this is the documented path,
# and it says what it cannot reach from here.
uninstall:
	rm -f $(BINDIR)/$(BIN)
	@echo "removed $(BINDIR)/$(BIN)"
	@echo "stored credentials remain at ~/.config/postflight; run 'postflight auth logout' before uninstalling to end the session, or let it expire"

clean:
	$(CARGO) clean

check:
	$(CARGO) test --locked
