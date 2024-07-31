
NAME=ratholego
BUILD_DIR=build
# GOBUILD=CGO_ENABLED=0 go build -trimpath -ldflags '-w -s -buildid='
GOBUILD=go build

normal: clean all

clean:
	rm -rf $(BUILD_DIR)/*

all: darwin-arm64 darwin-amd64 linux-amd64 windows-amd64

darwin-amd64:
	mkdir -p $(BUILD_DIR)/$@ 
	GOARCH=amd64 GOOS=darwin $(GOBUILD) -o $(BUILD_DIR)/$@/$(NAME)

darwin-arm64:
	mkdir -p $(BUILD_DIR)/$@ 
	GOARCH=arm64 GOOS=darwin $(GOBUILD) -o $(BUILD_DIR)/$@/$(NAME)

linux-amd64:
	mkdir -p $(BUILD_DIR)/$@
	GOARCH=amd64 GOOS=linux $(GOBUILD) -o $(BUILD_DIR)/$@/$(NAME)

linux-arm64:
	mkdir -p $(BUILD_DIR)/$@
	GOARCH=arm64 GOOS=linux $(GOBUILD) -o $(BUILD_DIR)/$@/$(NAME)

windows-amd64:
	mkdir -p $(BUILD_DIR)/$@
	GOARCH=amd64 GOOS=windows $(GOBUILD) -o $(BUILD_DIR)/$@/$(NAME).exe

run-server:
	mkdir -p build
	go build -o build/ratholego main.go
	GODEBUG='gctrace=1' ./build/ratholego --server local/server.toml

run-client:
	mkdir -p build
	go build -o build/ratholego main.go
	GODEBUG='gctrace=1' ./build/ratholego --client local/client.toml