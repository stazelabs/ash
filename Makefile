.PHONY: all clean restart install uninstall bench bench-baseline vocab vocab-check

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

# bench: run the canonical case list with repeat=5/warmup=2 and dump the
# raw JSON to bench/latest.json. The file is gitignored — it is a
# transient artifact, not the baseline contract. Use this when you want
# numbers without touching the checked-in baseline.
bench: bin/ash
	@mkdir -p bench
	./bin/ash bench --repeat 5 --warmup 2 --format json > bench/latest.json
	@echo "wrote bench/latest.json"

# bench-baseline: regenerate the canonical bench/baseline.json,
# bench/baseline.md, and bench/latency-snapshot.json. These ARE checked
# in — review the diff before committing. The fresh run is also
# persisted to .ash/ledger.db.
bench-baseline: bin/ash
	./bin/ash bench --repeat 5 --warmup 2 --record_baseline true
	@echo "review the diff: git diff bench/"

# vocab: regenerate docs/vocab/inventory.{md,json} — the checked-in
# inventory of every stable agent-facing string in ash (ASH-102). Run
# this after editing a verb's schema, adding/renaming an error code,
# or changing a pretty header/label. The companion target vocab-check
# runs the same generation and fails on drift; CI runs vocab-check.
bin/ashvocab: $(shell find cmd/ashvocab internal -name '*.go')
	go build -o bin/ashvocab ./cmd/ashvocab

vocab: bin/ashvocab
	./bin/ashvocab gen

vocab-check: bin/ashvocab
	./bin/ashvocab check
