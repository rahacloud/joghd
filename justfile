default:
    @just --list

# build joghd binary
build:
    @echo '{{ BOLD + CYAN }}Building Joghd!{{ NORMAL }}'
    go build -o joghd ./cmd/joghd

# update go packages
update:
    @cd ./cmd/joghd && go get -u

# run tests
test:
    go test -v ./... -covermode=atomic -coverprofile=coverage.out

# run golangci-lint
lint:
    golangci-lint run
