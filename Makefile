.PHONY: all clean

all: bin/ash bin/ashd

bin/ash: $(shell find cmd/ash internal -name '*.go')
	go build -o bin/ash ./cmd/ash

bin/ashd: $(shell find cmd/ashd internal -name '*.go')
	go build -o bin/ashd ./cmd/ashd

clean:
	rm -f bin/ash bin/ashd
