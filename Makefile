.PHONY: all clean restart install uninstall

PREFIX ?= $(HOME)/.local/bin

all: bin/ash bin/ashd

bin/ash: $(shell find cmd/ash internal -name '*.go')
	go build -o bin/ash ./cmd/ash

bin/ashd: $(shell find cmd/ashd internal -name '*.go')
	go build -o bin/ashd ./cmd/ashd

restart: all
	pkill -f bin/ashd 2>/dev/null || true

# install: symlink ash and ashd into $(PREFIX) so target repos can use them
# from $PATH. Symlinks (not copies) mean a rebuild here updates every target;
# the daemon auto-restarts on stale-binary detection (see killStaleIfNeeded).
install: all
	mkdir -p $(PREFIX)
	ln -sf $(CURDIR)/bin/ash  $(PREFIX)/ash
	ln -sf $(CURDIR)/bin/ashd $(PREFIX)/ashd
	@echo "installed: $(PREFIX)/{ash,ashd}"
	@case ":$$PATH:" in *":$(PREFIX):"*) ;; *) echo "warning: $(PREFIX) is not on PATH" ;; esac

uninstall:
	rm -f $(PREFIX)/ash $(PREFIX)/ashd

clean:
	rm -f bin/ash bin/ashd
