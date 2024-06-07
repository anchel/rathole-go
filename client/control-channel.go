package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/anchel/rathole-go/common"
	"github.com/anchel/rathole-go/config"
)

type ControlChannel struct {
	svcName       string
	shutdown_chan chan bool
	clientConfig  *config.ClientConfig
	svcConfig     *config.ClientServiceConfig
}

type RunDataChannelArgs struct {
	clientConfig *config.ClientConfig
	svcConfig    *config.ClientServiceConfig
	sessionKey   string
}

func NewControlChannel(svcName string, clientConfig *config.ClientConfig, svcConfig *config.ClientServiceConfig) *ControlChannel {
	return &ControlChannel{
		svcName:       svcName,
		shutdown_chan: make(chan bool),
		clientConfig:  clientConfig,
		svcConfig:     svcConfig,
	}
}

func (cc *ControlChannel) Run(parentCtx context.Context) {
	cancelCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	var conn net.Conn
	for i := 0; i < 3; i++ {
		con, err := net.Dial("tcp", cc.clientConfig.RemoteAddr)
		if err != nil {
			fmt.Println("server connect fail", err)
			continue
		} else {
			conn = con
			break
		}
	}

	if conn == nil {
		fmt.Println("server retry fail ")
		return
	}

	// defer conn.Close()

	digest := common.CalSha256(cc.svcName)
	fmt.Println("digest", digest)
	req, err := http.NewRequest("GET", "/control/hello", nil)
	if err != nil {
		fmt.Println("create request /control/hello fail", err)
		return
	}
	req.Header.Set("service", digest)
	err = req.Write(conn)
	// n, err := conn.Write([]byte(digest))
	if err != nil {
		fmt.Println("send request /control/hello fail", err)
		return
	}
	fmt.Println("send request /control/hello success")

	br := bufio.NewReader(conn)

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		fmt.Println("recv response /control/hello fail", err)
		return
	}
	// fmt.Println("recv response /control/hello", resp)
	if resp.StatusCode != http.StatusOK {
		fmt.Println("recv response /control/hello not ok", resp.StatusCode)
		return
	}
	nonce := resp.Header.Get("nonce")
	if nonce == "" {
		fmt.Println("recv response /control/hello not ok, no nonce")
		return
	}

	session_key := common.CalSha256(cc.svcConfig.Token + nonce)
	req, err = http.NewRequest("GET", "/control/auth", nil)
	if err != nil {
		fmt.Println("create request /control/auth fail", err)
		return
	}
	req.Header.Set("session_key", session_key)

	err = req.Write(conn)
	if err != nil {
		fmt.Println("send request /control/auth fail", err)
		return
	}
	fmt.Println("send request /control/auth success")

	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		fmt.Println("recv response /control/auth fail", err)
		return
	}
	// fmt.Println("recv response /control/auth", resp)
	if resp.StatusCode != http.StatusOK { // 401-token不正确，拒绝访问
		fmt.Println("recv response /control/auth not ok", resp.StatusCode)
		return
	}

	link_chan := make(chan string, 1)
	err_chan := make(chan error, 1)

	go func() {
		for {
			resp, err = http.ReadResponse(br, nil)
			if err != nil {
				fmt.Println("recv response /control/cmd fail", err)
				err_chan <- err
				break
			} else {
				go func() {
					link_chan <- resp.Header.Get("cmd")
				}()
			}
		}
	}()

OUTER:
	for {
		select {
		case cmd := <-link_chan:
			fmt.Println("cc read cmd", cmd)
			if cmd == "datachannel" {
				fmt.Println("cc server send create datachannel")
				args := RunDataChannelArgs{
					clientConfig: cc.clientConfig,
					svcConfig:    cc.svcConfig,
					sessionKey:   session_key,
				}
				go run_data_channel(args)
			} else { // "heartbeat"
				fmt.Println("cc server send heartbeat")
			}
		case <-err_chan:
			fmt.Println("cc select error")
			break OUTER
		case <-cancelCtx.Done():
			fmt.Println("cc receive cancel")
			break OUTER
		case <-time.After(60 * time.Second):
			fmt.Println("cc heartbeat timeout")
			break OUTER
		}
	}
}

func run_data_channel(args RunDataChannelArgs) error {
	var conn *net.TCPConn
	var err error
	tcpAdr, _ := net.ResolveTCPAddr("tcp", args.clientConfig.RemoteAddr)

	for i := 0; i < 3; i++ {
		con, err := net.DialTCP("tcp", nil, tcpAdr)
		if err != nil {
			fmt.Println("datachannel server connect fail", err)
			continue
		} else {
			conn = con
			break
		}
	}

	if conn == nil {
		fmt.Println("server retry fail ")
		return err
	}

	// defer conn.Close() // 关闭连接

	req, err := http.NewRequest("GET", "/data/hello", nil)
	if err != nil {
		fmt.Println("create request /data/hello fail", err)
		return err
	}
	req.Header.Set("session_key", args.sessionKey)
	err = req.Write(conn)
	if err != nil {
		fmt.Println("send request /data/hello fail", err)
		return err
	}
	fmt.Println("send request /data/hello success")

	// 接下来读两个字节，第一个字节：0-fail 1-ok；第二个字节：1-tcp 2-udp
	// rconn := common.NewRewindConn(conn)
	// rconn.SetBufferSize(128)

	var buf [2]byte
	n, err := io.ReadFull(conn, buf[:])
	if err != nil {
		fmt.Println("recv response /data/hello fail", err)
		return err
	}
	if n != len(buf) {
		fmt.Println("recv response /data/hello byte count not correct", n, len(buf))
		return fmt.Errorf("recv response /data/hello byte count not correct, %d, %d", n, len(buf))
	}
	if buf[0] != 1 {
		fmt.Println("recv response /data/hello not ok", buf)
		return errors.New("recv response /data/hello not ok")
	}

	forwardType := "tcp"
	if buf[1] == 2 {
		forwardType = "udp"
	}
	// resp, err := http.ReadResponse(bufio.NewReader(rconn), nil)
	// if err != nil {
	// 	fmt.Println("recv response /data/hello fail", err)
	// 	return err
	// }

	// if resp.StatusCode != 200 { // 401 - token invalid
	// 	fmt.Println("recv response /data/hello not ok", resp.StatusCode)
	// 	return errors.New("recv response /data/hello not ok")
	// }

	// forwardType := resp.Header.Get("type") // tcp udp

	if forwardType != string(args.svcConfig.Type) {
		fmt.Println("forward type not equal", forwardType, args.svcConfig.Type)
		return errors.New("forward type not equal")
	}

	fmt.Println("recv response /data/hello ok", forwardType, buf)
	// rconn.Rewind()
	// rconn.StopBuffering()
	// rconn.Discard(2)
	// common.DiscardRewindConn(rconn, resp)

	if forwardType == "tcp" {
		forward_data_channel_for_tcp(conn, args.svcConfig.LocalAddr)
	} else if forwardType == "udp" {
		forward_data_channel_for_udp(conn, args.svcConfig.LocalAddr)
	} else {
		fmt.Println("unknown forward type", forwardType)
	}

	return nil
}

func forward_data_channel_for_tcp(remoteConn *net.TCPConn, localAddr string) {
	tcpAdr, _ := net.ResolveTCPAddr("tcp", localAddr)
	clientConn, err := net.DialTCP("tcp", nil, tcpAdr)
	if err != nil {
		fmt.Println("connect localaddr fail", err)
		return
	}
	// defer clientConn.Close()
	fmt.Println("forward_data_channel_for_tcp", remoteConn.LocalAddr())

	// for {
	// 	buf := make([]byte, 1024)
	// 	n, e := remoteConn.Read(buf)
	// 	fmt.Println("remoteConn Read", n, e)
	// 	if e != nil {
	// 		fmt.Println("remoteConn Read error", e)
	// 		break
	// 	}
	// 	if n > 0 {
	// 		fmt.Println("remoteConn Read", string(buf[:n]))
	// 		wn, e := clientConn.Write(buf)
	// 		fmt.Println("remoteConn Read and Write to client", wn, e)
	// 	}
	// }

	err = common.CopyTcpConnection(clientConn, remoteConn)
	if err != nil {
		fmt.Println("CopyTcpConnection error", err)
	} else {
		fmt.Println("CopyTcpConnection success", err)
	}
}

func forward_data_channel_for_udp(conn net.Conn, localAddr string) {

}
