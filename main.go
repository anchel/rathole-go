package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/anchel/rathole-go/client"
	util "github.com/anchel/rathole-go/common"
	"github.com/anchel/rathole-go/config"
	"github.com/anchel/rathole-go/pb/basic/basicpb"
	"github.com/anchel/rathole-go/server"
	"github.com/davecgh/go-spew/spew"
	"google.golang.org/protobuf/proto"
)

func main() {
	cliArgs, err := util.GetCliArgs()
	// spew.Dump(cliArgs)
	if err != nil {
		fmt.Println(err)
		return
	}
	runMode, err := cliArgs.GetRunMode()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("runmode", runMode)

	switch runMode {
	case util.RUN_CLIENT:
		fmt.Println("run as a client")
		run_client(cliArgs)
	case util.RUN_SERVER:
		fmt.Println("run as a server")
		run_server(cliArgs)
	default:
		fmt.Println("unknown runmode")
	}
}

func run_client(args *util.CliArgs) {
	client := client.NewClient(&args.Config.Client)
	client.Run()
}

func run_server(args *util.CliArgs) {
	server := server.NewServer(&args.Config.Server)
	server.Run()
}

func testProto() {
	m := &basicpb.HelloMessage{
		Ver:    1,
		Digest: "hello",
	}
	out, err := proto.Marshal(m)
	if err != nil {
		fmt.Println("Marshal error", err)
		return
	}
	fmt.Println(out)
}

func testLoadToml() {
	content, err := os.ReadFile("local/client.toml")
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
