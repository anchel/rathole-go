package common

import (
	"bufio"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/anchel/rathole-go/internal/config"
)

type RUN_MODE uint8

const RUN_UNKNOWN RUN_MODE = 0
const RUN_CLIENT RUN_MODE = 1
const RUN_SERVER RUN_MODE = 2

// ErrUnauthorized 表示服务端明确拒绝了本次认证（token 配置错误），
// 和网络抖动不是一回事，调用方不应该把它当成可重试的临时故障。
var ErrUnauthorized = errors.New("unauthorized: token incorrect")

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

// RandString 生成握手用的 nonce。
// 必须用 crypto/rand：math/rand 的输出被观测若干次后内部状态即可被还原，
// nonce 一旦可预测，攻击者就能等到某个已抓到 session_key 的 nonce 复现时重放认证。
func RandString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
		Debug("datachannel src forward to dst", written, err)
		chan_src_to_dst <- err
	}()

	go func() {
		written, err := io.Copy(src, dst)
		Debug("datachannel dst forward to src", written, err)
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
					Debug("local to remote normal end, CloseWrite remote error", e)
				}
			}

			if done1 && done2 {
				break label_for
			}

		case err1 = <-chan_src_to_dst:
			done1 = true

			if (err1 == nil || IsNetErrorRead(err1)) && !done2 {
				if e := dst.CloseWrite(); e != nil {
					Debug("remote to local normal end, CloseWrite local error", e)
				}
			}

			if done1 && done2 {
				break label_for
			}
		}
	}
	if err1 != nil && err2 != nil {
		return errors.Join(err1, err2)
	}
	if err1 != nil {
		return err1
	}
	return err2
}

func Do_control_channel_handshake(conn net.Conn, br *bufio.Reader, svcName string, token string) (string, error) {

	digest := CalSha256(svcName)
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
		return "", fmt.Errorf("cc recv response /control/hello not ok, status %d", resp.StatusCode)
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

	// 这里以前 return "", err，而 err 此刻是 nil，
	// 认证失败被当成了成功，客户端会带着空 session_key 继续跑下去。
	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Println("cc recv response /control/auth unauthorized, 请检查 token 配置")
		return "", ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Println("cc recv response /control/auth not ok", resp.StatusCode)
		return "", fmt.Errorf("cc recv response /control/auth not ok, status %d", resp.StatusCode)
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
	Debug("datachannel send request /data/hello success")

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

// SendError 把错误投递给监听方；err_chan 为 nil 时直接返回，
// 否则会在 nil channel 上永久阻塞，goroutine 再也退不出来。
func SendError(ctx context.Context, err_chan chan error, err error) {
	if err_chan == nil {
		return
	}
	select {
	case <-ctx.Done():
	case err_chan <- err:
	}
}
