package common

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
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
	isServer := flag.Bool("server", false, "-server run as a server")
	isClient := flag.Bool("client", false, "-client run as a client")
	flag.Parse()
	args := flag.Args()
	fmt.Println("args", args)
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

func CopyTcpConnection(dst net.Conn, src net.Conn) error {
	chan_remote_to_local := make(chan error)
	chan_local_to_remote := make(chan error)

	go func() {
		written, err := io.Copy(dst, src)
		fmt.Println("datachannel remote forward to local", written, err)
		chan_remote_to_local <- err
	}()

	go func() {
		written, err := io.Copy(src, dst)
		fmt.Println("datachannel local forward to remote", written, err)
		chan_local_to_remote <- err
	}()

	var err1, err2 error
	var errCount uint8 = 0
label_for:
	for {
		select {
		case e1 := <-chan_remote_to_local:
			err1 = e1
			errCount++
			if err1 == nil {
				dst.Close()
			}
			if errCount >= 2 {
				break label_for
			}
		case e2 := <-chan_local_to_remote:
			err2 = e2
			errCount++
			if err2 == nil {
				src.Close()
			}
			if errCount >= 2 {
				break label_for
			}
		}
	}
	if err1 != nil && err2 != nil {
		return errors.New(err1.Error() + err2.Error())
	}
	if err1 != nil {
		return err1
	}
	return err2
}

func DiscardRewindConn(rconn *RewindConn, resp *http.Response) error {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	err := resp.Write(buf)
	if err != nil {
		fmt.Println("DiscardRewindConn error", err)
		return err
	}
	fmt.Println("ready to discard:", buf.Len())
	n, err := rconn.Discard(buf.Len())
	if err != nil {
		return err
	}
	if n != buf.Len() {
		return fmt.Errorf("discard n not equal, %d, %d", n, buf.Len())
	}
	return nil
}
