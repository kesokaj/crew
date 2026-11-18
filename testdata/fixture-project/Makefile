# The fixture project's two commands (design 15.1): the setup a sandbox runs first, and the tests.
.PHONY: setup test
setup:
	go mod download
	go vet ./...

test:
	go test -count=1 ./...
