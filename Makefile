# owlwatch — thin convenience wrapper. The web build must run before any Go
# build: web/embed.go go:embeds web/dist, which is gitignored.

.PHONY: build web test run docker clean

build: web
	go build -o owlwatch ./cmd/owlwatch

web:
	npm ci --prefix web
	npm run build --prefix web

test: web
	go vet ./...
	go test ./...

run: build
	./owlwatch

docker:
	docker build -t owlwatch:dev .

clean:
	rm -f owlwatch
	rm -rf web/dist
