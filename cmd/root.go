package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/anchel/rathole-go/internal/client"
	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
	"github.com/anchel/rathole-go/internal/server"
	"github.com/spf13/cobra"
)

var (
	isServer bool
	isClient bool
)

var rootCmd = &cobra.Command{
	Use:   "ratholego",
	Short: "nat proxy",
	Long:  "nat proxy, from public network to private network",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) <= 0 {
			fmt.Println("配置文件路径为空")
			os.Exit(1)
		}
		err := Run(args[0])
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&isServer, "server", "s", false, "run as a server")
	rootCmd.PersistentFlags().BoolVarP(&isClient, "client", "c", false, "run as a client")
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func Run(configPath string) error {
	conf, err := common.GetConfig(configPath)
	if err != nil {
		return errors.New("读取配置失败")
	}

	runMode, err := common.GetRunMode(isServer, isClient, conf)
	if err != nil {
		fmt.Println(err)
		return err
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	switch runMode {
	case common.RUN_CLIENT:
		fmt.Println("run as a client")
		run_client(c, conf)
	case common.RUN_SERVER:
		fmt.Println("run as a server")
		run_server(c, conf)
	default:
		fmt.Println("unknown runmode")
		err = errors.New("unknown runmode")
	}
	return err
}

func run_client(c chan os.Signal, conf *config.Config) {
	client := client.NewClient(&conf.Client)
	client.Run(c)
}

func run_server(c chan os.Signal, conf *config.Config) {
	server := server.NewServer(&conf.Server)
	server.Run(c)
}
