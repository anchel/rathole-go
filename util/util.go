package util

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/anchel/rathole-go/config"
)

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

type RUN_MODE uint8

const RUN_UNKNOWN RUN_MODE = 0
const RUN_CLIENT RUN_MODE = 1
const RUN_SERVER RUN_MODE = 2

type CliArgs struct {
	Server     bool
	Client     bool
	ConfigPath string
	Config     *config.Config
}

func (ca *CliArgs) GetRunMode() (RUN_MODE, error) {

	if ca.Client {
		if ca.Config.Client.RemoteAddr != "" {
			return RUN_CLIENT, nil
		} else {
			return RUN_UNKNOWN, errors.New("指定为客户端，但配置文件错误")
		}
	} else if ca.Server {
		if ca.Config.Server.BindAddr != "" {
			return RUN_SERVER, nil
		} else {
			return RUN_UNKNOWN, errors.New("指定为服务器端，但配置文件错误")
		}
	} else {
		if ca.Config.Client.RemoteAddr != "" {
			return RUN_CLIENT, nil
		} else if ca.Config.Server.BindAddr != "" {
			return RUN_SERVER, nil
		}
	}
	return RUN_UNKNOWN, errors.New("不能判断出运行模式")
}

func GetCliArgs() (*CliArgs, error) {
	isServer := flag.Bool("server", false, "--server run as a server")
	isClient := flag.Bool("client", false, "--server run as a client")
	flag.Parse()
	args := flag.Args()
	if len(args) <= 0 {
		return nil, errors.New("未指定配置文件")
	}

	content, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Println("ReadFile error", err)
		return nil, errors.New("加载配置文件失败")
	}
	var conf config.Config
	_, err = toml.Decode(string(content), &conf)
	if err != nil {
		fmt.Println("Decode error", err)
		return nil, errors.New("解析配置文件失败")
	}

	return &CliArgs{
		Server:     *isServer,
		Client:     *isClient,
		ConfigPath: args[0],
		Config:     &conf,
	}, nil
}

func RandStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func ResponseWriteWithBuffered(src *http.Response, dst io.Writer) (err error) {
	writer := bufio.NewWriter(dst)
	defer writer.Flush()

	err = src.Write(writer)
	return
}
