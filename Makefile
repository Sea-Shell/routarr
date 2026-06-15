build:
	go build -o bin/routarr ./cmd/routarr

test:
	go test -v ./...

run: build
	./bin/routarr

clean:
	rm -rf bin/
