.PHONY: all clean restart

all: bin/ash bin/ashd

bin/ash: $(shell find cmd/ash internal -name '*.go')
	go build -o bin/ash ./cmd/ash

bin/ashd: $(shell find cmd/ashd internal -name '*.go')
	go build -o bin/ashd ./cmd/ashd

restart: all
	pkill -f bin/ashd 2>/dev/null || true

clean:
	rm -f bin/ash bin/ashd
