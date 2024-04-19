package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/anchel/rathole-go/config"
	"github.com/davecgh/go-spew/spew"
)

func main() {
	testLoadToml()
}

func testLoadToml() {
	content, err := os.ReadFile("local/server.toml")
	if err != nil {
		fmt.Println("ReadFile error", err)
		return
	}
	var conf config.Config
	_, err = toml.Decode(string(content), &conf)
	if err != nil {
		fmt.Println("Decode error", err)
		return
	}
	spew.Dump(conf)
}
