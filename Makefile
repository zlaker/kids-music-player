.PHONY: build linux test run

build:
	go build -o bin/kids-music-player ./cmd/kids-music-player

linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-s -w' -o bin/kids-music-player-linux-amd64 ./cmd/kids-music-player

test:
	go test ./...

run: build
	./bin/kids-music-player -music ./music
