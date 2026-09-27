# owlwatch — thin convenience wrapper. The web build must run before any Go
# build: web/embed.go go:embeds web/dist, which is gitignored.

.PHONY: build web test run docker clean

build: web
	go build -o owlwatch ./cmd/owlwatch

web:
	cd web && bun install --frozen-lockfile
	cd web && bun run build

test: web
	cd web && bun run test
	go vet ./...
	go test ./... -race

run: build
	./owlwatch

docker:
	docker build -t owlwatch:dev .

clean:
	rm -f owlwatch
	rm -rf web/dist
