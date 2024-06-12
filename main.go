package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/anchel/rathole-go/client"
	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/server"
)

func main() {
	cliArgs, err := common.GetCliArgs()
	if err != nil {
		fmt.Println(err)
		return
	}
	runMode, err := cliArgs.GetRunMode()
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("runmode", runMode)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	switch runMode {
	case common.RUN_CLIENT:
		fmt.Println("run as a client")
		run_client(c, cliArgs)
	case common.RUN_SERVER:
		fmt.Println("run as a server")
		run_server(c, cliArgs)
	default:
		fmt.Println("unknown runmode")
	}
}

func run_client(c chan os.Signal, args *common.CliArgs) {
	client := client.NewClient(&args.Config.Client)
	client.Run(c)
}

func run_server(c chan os.Signal, args *common.CliArgs) {
	server := server.NewServer(&args.Config.Server)
	server.Run(c)
}
