build:
	go build -o bin/yt2sp ./cmd/yt2sp

test:
	go test -v ./...

run: build
	./bin/yt2sp

clean:
	rm -rf bin/
