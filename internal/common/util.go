package common

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/anchel/rathole-go/internal/config"
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

func GetRunMode(isServer, isClient bool, config *config.Config) (RUN_MODE, error) {
	if isClient {
		if config.Client.RemoteAddr != "" {
			return RUN_CLIENT, nil
		} else {
			return RUN_UNKNOWN, errors.New("指定为客户端，但配置文件错误")
		}
	} else if isServer {
		if config.Server.BindAddr != "" {
			return RUN_SERVER, nil
		} else {
			return RUN_UNKNOWN, errors.New("指定为服务器端，但配置文件错误")
		}
	} else {
		if config.Client.RemoteAddr != "" {
			return RUN_CLIENT, nil
		} else if config.Server.BindAddr != "" {
			return RUN_SERVER, nil
		}
	}
	return RUN_UNKNOWN, errors.New("不能判断出运行模式")
}

func GetConfig(p string) (*config.Config, error) {
	content, err := os.ReadFile(p)
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
	return &conf, nil
}

func RandStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func ResponseWriteWithBuffered(src *http.Response, dst io.Writer) error {
	writer := bufio.NewWriter(dst)
	err := src.Write(writer)
	if err != nil {
		return err
	}
	err = writer.Flush()
	return err
}

type ReadCloseWriter interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	CloseWrite() error
}

func UnwrapNetError(err error) error {
	werr := errors.Unwrap(err)
	lastErr := err
	for werr != nil && IsNetError(werr) {
		lastErr = werr
		werr = errors.Unwrap(werr)
	}
	return lastErr
}

func IsNetErrorRead(err error) bool {
	werr := UnwrapNetError(err)
	e, ok := werr.(*net.OpError)
	if ok && e.Op == "read" {
		return true
	}
	return false
}

func IsNetErrorWrite(err error) bool {
	werr := UnwrapNetError(err)
	e, ok := werr.(*net.OpError)
	if ok && e.Op == "write" {
		return true
	}
	return false
}

func IsNetError(err error) bool {
	_, ok := err.(*net.OpError)
	return ok
}

func CopyTcpConnection(ctx context.Context, dst ReadCloseWriter, src ReadCloseWriter) error {
	chan_src_to_dst := make(chan error, 1)
	chan_dst_to_src := make(chan error, 1)

	go func() {
		written, err := io.Copy(dst, src)
		fmt.Println("datachannel src forward to dst", written, err)
		chan_src_to_dst <- err
	}()

	go func() {
		written, err := io.Copy(src, dst)
		fmt.Println("datachannel dst forward to src", written, err)
		chan_dst_to_src <- err
	}()

	var done1, done2 bool
	var err1, err2 error

label_for:
	for {
		select {
		case <-ctx.Done():
			break label_for

		case err2 = <-chan_dst_to_src:
			done2 = true

			if (err2 == nil || IsNetErrorRead(err2)) && !done1 {
				if e := src.CloseWrite(); e != nil {
					fmt.Println("local to remote normal end, CloseWrite remote error", e)
				}
			}

			if done1 && done2 {
				break label_for
			}

		case err1 = <-chan_src_to_dst:
			done1 = true

			if (err1 == nil || IsNetErrorRead(err1)) && !done2 {
				if e := dst.CloseWrite(); e != nil {
					fmt.Println("remote to local normal end, CloseWrite local error", e)
				}
			}

			if done1 && done2 {
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

func Do_control_channel_handshake(conn net.Conn, br *bufio.Reader, svcName string, token string) (string, error) {

	digest := CalSha256(svcName)
	fmt.Println("digest", digest)
	req, err := http.NewRequest("GET", "/control/hello", nil)
	if err != nil {
		fmt.Println("cc create request /control/hello fail", err)
		return "", err
	}
	req.Header.Set("service", digest)
	err = req.Write(conn)
	if err != nil {
		fmt.Println("cc send request /control/hello fail", err)
		return "", err
	}
	fmt.Println("cc send request /control/hello success")

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		fmt.Println("cc recv response /control/hello fail", err)
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Println("cc recv response /control/hello not ok", resp.StatusCode)
		return "", errors.New("cc recv response /control/hello not ok")
	}
	nonce := resp.Header.Get("nonce")
	if nonce == "" {
		fmt.Println("cc recv response /control/hello not ok, no nonce")
		return "", errors.New("cc recv response /control/hello not ok, no nonce")
	}

	session_key := CalSha256(token + nonce)
	req, err = http.NewRequest("GET", "/control/auth", nil)
	if err != nil {
		fmt.Println("cc create request /control/auth fail", err)
		return "", err
	}
	req.Header.Set("session_key", session_key)

	err = req.Write(conn)
	if err != nil {
		fmt.Println("cc send request /control/auth fail", err)
		return "", err
	}
	fmt.Println("cc send request /control/auth success")

	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		fmt.Println("cc recv response /control/auth fail", err)
		return "", err
	}

	if resp.StatusCode != http.StatusOK { // 401-token不正确，拒绝访问
		fmt.Println("cc recv response /control/auth not ok", resp.StatusCode, session_key)
		return "", err
	}

	return session_key, nil
}

func Do_data_channel_handshake(conn net.Conn, sessionKey string, svcType config.ServiceType) (string, error) {
	req, err := http.NewRequest("GET", "/data/hello", nil)
	if err != nil {
		fmt.Println("datachannel create request /data/hello fail", err)
		return "", err
	}
	req.Header.Set("session_key", sessionKey)
	err = req.Write(conn)
	if err != nil {
		fmt.Println("datachannel send request /data/hello fail", err)
		return "", err
	}
	fmt.Println("datachannel send request /data/hello success")

	respbuf := [...]byte{0, 0}
	_, err = io.ReadFull(conn, respbuf[:])
	if err != nil {
		fmt.Println("datachannel recv response /data/hello fail", err)
		return "", err
	}

	if respbuf[0] != 1 {
		fmt.Println("datachannel recv response /data/hello not ok", respbuf)
		return "", errors.New("recv response /data/hello not ok")
	}

	forwardType := config.ServiceType("")
	switch respbuf[1] {
	case 1:
		forwardType = config.TCP
	case 2:
		forwardType = config.UDP
	}

	if forwardType != svcType {
		fmt.Println("datachannel forward type not equal", forwardType, svcType)
		return "", errors.New("forward type not equal")
	}

	return string(forwardType), nil
}

func SendError(ctx context.Context, err_chan chan error, err error) {
	select {
	case <-ctx.Done():
	case err_chan <- err:
	}
}
