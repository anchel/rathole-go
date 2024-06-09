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

	"github.com/anchel/rathole-go/config"
	"github.com/anchel/rathole-go/internal/common"
)

type ControlChannel struct {
	svcName string

	clientConfig *config.ClientConfig
	svcConfig    *config.ClientServiceConfig

	client *Client

	cancelCtx context.Context
	cancel    context.CancelFunc
	err       error
}

type RunDataChannelArgs struct {
	clientConfig *config.ClientConfig
	svcConfig    *config.ClientServiceConfig
	sessionKey   string
}

func NewControlChannel(client *Client, svcName string, clientConfig *config.ClientConfig, svcConfig *config.ClientServiceConfig) *ControlChannel {
	cancelCtx, cancel := context.WithCancel(client.cancelCtx)

	return &ControlChannel{
		svcName:      svcName,
		client:       client,
		clientConfig: clientConfig,
		svcConfig:    svcConfig,
		cancelCtx:    cancelCtx,
		cancel:       cancel,
	}
}

func (cc *ControlChannel) Close() {
	fmt.Println("client cc close")
	cc.err = errors.New("client cc close")
	cc.cancel()
}

func (cc *ControlChannel) Run() {
	if cc.err != nil {
		return
	}

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
		cc.Reconnect()
		return
	}

	defer func() {
		fmt.Println("controlchannel conn.Close")
		defer conn.Close()
	}()

	digest := common.CalSha256(cc.svcName)
	fmt.Println("digest", digest)
	req, err := http.NewRequest("GET", "/control/hello", nil)
	if err != nil {
		fmt.Println("create request /control/hello fail", err)
		return
	}
	req.Header.Set("service", digest)
	err = req.Write(conn)
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

	needReconnect := false

OUTER:
	for {
		select {
		case cmd := <-link_chan:
			// fmt.Println("cc read cmd", cmd)
			if cmd == "datachannel" {
				fmt.Println("cc server send create datachannel")
				args := RunDataChannelArgs{
					clientConfig: cc.clientConfig,
					svcConfig:    cc.svcConfig,
					sessionKey:   session_key,
				}

				go create_data_channel(cc.cancelCtx, args)
			} else { // "heartbeat"
				fmt.Println("cc server send heartbeat")
			}
		case <-err_chan:
			fmt.Println("cc select error")
			needReconnect = true
			break OUTER
		case <-cc.cancelCtx.Done():
			fmt.Println("cc receive cancel")
			break OUTER
		case <-time.After(60 * time.Second):
			fmt.Println("cc heartbeat timeout")
			needReconnect = true
			break OUTER
		}
	}

	// 重连
	if needReconnect {
		cc.Reconnect()
	}
}

func (cc *ControlChannel) Reconnect() {
	fmt.Println("cc need reconnect", cc.svcName)
	time.Sleep(5 * time.Second)
	cc.client.svc_chan <- cc.svcName
}

func create_data_channel(parentCtx context.Context, args RunDataChannelArgs) error {
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

	fmt.Println("datachannel connected to server, LocalAddr:", conn.LocalAddr())

	defer func() {
		fmt.Println("datachannel conn.Close")
		conn.Close() // 关闭连接
	}()

	myconn := common.NewMyConn(conn)

	req, err := http.NewRequest("GET", "/data/hello", nil)
	if err != nil {
		fmt.Println("create request /data/hello fail", err)
		return err
	}
	req.Header.Set("session_key", args.sessionKey)
	err = req.Write(myconn)
	if err != nil {
		fmt.Println("send request /data/hello fail", err)
		return err
	}
	fmt.Println("send request /data/hello success")

	respbuf := [...]byte{0, 0}
	_, err = io.ReadFull(myconn, respbuf[:])
	if err != nil {
		fmt.Println("recv response /data/hello fail", err)
		return err
	}

	if respbuf[0] != 1 {
		fmt.Println("recv response /data/hello not ok", respbuf)
		return errors.New("recv response /data/hello not ok")
	}

	forwardType := config.ServiceType("")
	switch respbuf[1] {
	case 1:
		forwardType = config.TCP
	case 2:
		forwardType = config.UDP
	}

	if forwardType != args.svcConfig.Type {
		fmt.Println("forward type not equal", forwardType, args.svcConfig.Type)
		return errors.New("forward type not equal")
	}

	fmt.Println("recv response /data/hello ok", forwardType, respbuf)

	if forwardType == "tcp" {
		forward_data_channel_for_tcp(parentCtx, myconn, args.svcConfig.LocalAddr)
	} else if forwardType == "udp" {
		forward_data_channel_for_udp(parentCtx, conn, args.svcConfig.LocalAddr)
	} else {
		fmt.Println("unknown forward type", forwardType)
	}

	return nil
}

func forward_data_channel_for_tcp(ctx context.Context, remoteConn *common.MyConn, localAddr string) {
	tcpAdr, _ := net.ResolveTCPAddr("tcp", localAddr)
	fmt.Println("forward_data_channel_for_tcp", tcpAdr, localAddr)
	clientConn, err := net.DialTCP("tcp", nil, tcpAdr)
	if err != nil {
		fmt.Println("connect localaddr fail", err)
		return
	}
	defer func() {
		fmt.Println("forward_data_channel_for_tcp clientConn.Close()")
		clientConn.Close()
	}()

	fmt.Println("forward_data_channel_for_tcp, clientConn.LocalAddr", clientConn.LocalAddr())

	err = common.CopyTcpConnection(ctx, clientConn, remoteConn)
	if err != nil {
		fmt.Println("CopyTcpConnection error", err)
	} else {
		fmt.Println("CopyTcpConnection success", err)
	}
}

func forward_data_channel_for_udp(ctx context.Context, conn *net.TCPConn, localAddr string) {

}
