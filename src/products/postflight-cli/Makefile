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

uninstall:
	rm -f $(BINDIR)/$(BIN)

clean:
	$(CARGO) clean

check:
	$(CARGO) test --locked
