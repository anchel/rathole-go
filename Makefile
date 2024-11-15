
NAME=ratholego
BUILD_DIR=build
GOBUILD=CGO_ENABLED=0 go build -trimpath -ldflags '-w -s -buildid='

normal: clean all

clean:
	rm -rf $(BUILD_DIR)

all: darwin-arm64 darwin-amd64 linux-amd64 windows-amd64

darwin-amd64:
	GOARCH=amd64 GOOS=darwin $(GOBUILD) -o $(BUILD_DIR)/$(NAME)-$@

darwin-arm64:
	GOARCH=arm64 GOOS=darwin $(GOBUILD) -o $(BUILD_DIR)/$(NAME)-$@

linux-amd64:
	GOARCH=amd64 GOOS=linux $(GOBUILD) -o $(BUILD_DIR)/$(NAME)-$@

linux-arm64:
	GOARCH=arm64 GOOS=linux $(GOBUILD) -o $(BUILD_DIR)/$(NAME)-$@

windows-amd64:
	GOARCH=amd64 GOOS=windows $(GOBUILD) -o $(BUILD_DIR)/$(NAME)-$@.exe

run-server:
	mkdir -p build
	go build -o build/ratholego main.go
	./build/ratholego --server config/server.sample.toml

run-client:
	mkdir -p build
	go build -o build/ratholego main.go
	./build/ratholego --client config/client.sample.toml
