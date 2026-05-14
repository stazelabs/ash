.PHONY: all clean restart install uninstall bench bench-baseline vocab vocab-check validate validate-check

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

# bin/encexplore: corpus/substitution/validate harness for token-shape work.
# Built on demand by `make validate`; not part of the default `all` target.
bin/encexplore: $(shell find cmd/encexplore internal -name '*.go')
	go build -o bin/encexplore ./cmd/encexplore

# validate: run encexplore's cl100k-vs-Claude cross-check (ASH-115). Sources
# .env and .env.local if present (.env.local wins, mirroring the dotenv
# convention) and errors clearly if ANTHROPIC_API_KEY is still unset. The
# model is pinned so the baseline doesn't drift between sessions. Output:
# testdata/validate_results.md.
#
# Cost: ~160 count_tokens calls in the default run, cached by body — usually
# many fewer. The endpoint is cheap, not free.
validate: bin/encexplore
	@set -e; \
	if [ -f .env ]; then set -a; . ./.env; set +a; fi; \
	if [ -f .env.local ]; then set -a; . ./.env.local; set +a; fi; \
	if [ -z "$$ANTHROPIC_API_KEY" ]; then \
		echo "error: ANTHROPIC_API_KEY not set. Copy .env.example to .env (or .env.local) and fill it in." >&2; \
		exit 1; \
	fi; \
	./bin/encexplore validate --model claude-sonnet-4-5 --out testdata/validate_results.md

# validate-check: gate the checked-in cross-validation artifact. Fails if any
# rule's cl100k Δ disagrees with its Claude Δ in sign (the `✗` marker in
# testdata/validate_results.md). File-only — no API key needed — so it can run
# in CI for free. Contributors still have to run `make validate` after a
# token-shape change for this gate to bite. Same contract as vocab-check.
validate-check:
	@if [ ! -f testdata/validate_results.md ]; then \
		echo "validate-check: testdata/validate_results.md missing. Run \`make validate\` to generate it." >&2; \
		exit 1; \
	fi
	@if grep -q '✗' testdata/validate_results.md; then \
		echo "validate-check: cl100k vs Claude sign disagreement in testdata/validate_results.md:" >&2; \
		grep '✗' testdata/validate_results.md >&2; \
		echo "" >&2; \
		echo "Run \`make validate\` after fixing the offending substitution rule." >&2; \
		exit 1; \
	fi
	@echo "validate-check: ok"
