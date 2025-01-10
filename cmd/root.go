package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/anchel/rathole-go/internal/client"
	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
	"github.com/anchel/rathole-go/internal/server"
	"github.com/anchel/rathole-go/internal/updater"
	"github.com/spf13/cobra"

	"net/http"
	_ "net/http/pprof"
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
			fmt.Println("The configuration file path is empty")
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
	config.SetConfigPath(configPath)

	conf, err := config.GetConfig()
	if err != nil {
		return errors.New("failed to read configuration")
	}

	runMode, err := common.GetRunMode(isServer, isClient, conf)
	if err != nil {
		fmt.Println(err)
		return err
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	updater, err := updater.GetUpdater(rootCtx, configPath)
	if err != nil {
		fmt.Println("create updater fail", err) // 创建热更新失败，不影响程序继续
	}

	switch runMode {
	case common.RUN_CLIENT:
		fmt.Println("run as a client")
		run_client(rootCtx, signalChan, updater, conf)
	case common.RUN_SERVER:
		fmt.Println("run as a server")
		go func() {
			log.Println(http.ListenAndServe("0.0.0.0:6060", nil))
		}()
		run_server(rootCtx, signalChan, updater, conf)
	default:
		fmt.Println("unknown runmode")
		err = errors.New("unknown runmode")
	}
	return err
}

func run_client(ctx context.Context, c chan os.Signal, updater chan *config.Config, conf *config.Config) {
	client := client.NewClient(ctx, &conf.Client)
	client.Run(c, updater)
}

func run_server(ctx context.Context, c chan os.Signal, updater chan *config.Config, conf *config.Config) {
	server := server.NewServer(ctx, &conf.Server)
	server.Run(c, updater)
}
