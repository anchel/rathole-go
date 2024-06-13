package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
)

type ControlChannel struct {
	svcName string

	clientConfig *config.ClientConfig
	svcConfig    *config.ClientServiceConfig

	client *Client

	cancelCtx context.Context
	cancel    context.CancelFunc
	err       error

	muDone   sync.Mutex
	canceled bool
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
	cc.Cancel()
}

func (cc *ControlChannel) Cancel() {
	cc.muDone.Lock()
	defer cc.muDone.Unlock()
	if !cc.canceled {
		cc.canceled = true
		cc.cancel()
	}
}

func (cc *ControlChannel) Run() {
	if cc.canceled {
		return
	}

	needReconnect := false // 是否重连
	defer func() {
		if needReconnect {
			go cc.Reconnect()
		}
	}()

	defer cc.Cancel()

	var conn net.Conn
	for i := 0; i < 1; i++ {
		con, err := net.Dial("tcp", cc.clientConfig.RemoteAddr)
		if err != nil {
			fmt.Println("cc connect server fail", err)
			continue
		} else {
			conn = con
			break
		}
	}

	if conn == nil {
		fmt.Println("cc server retry fail ")
		needReconnect = true
		return
	}

	defer func() {
		fmt.Println("cc conn.Close")
		conn.Close()
	}()

	digest := common.CalSha256(cc.svcName)
	fmt.Println("digest", digest)
	req, err := http.NewRequest("GET", "/control/hello", nil)
	if err != nil {
		fmt.Println("cc create request /control/hello fail", err)
		return
	}
	req.Header.Set("service", digest)
	err = req.Write(conn)
	if err != nil {
		fmt.Println("cc send request /control/hello fail", err)
		return
	}
	fmt.Println("cc send request /control/hello success")

	br := bufio.NewReader(conn)

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		fmt.Println("cc recv response /control/hello fail", err)
		return
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Println("cc recv response /control/hello not ok", resp.StatusCode)
		return
	}
	nonce := resp.Header.Get("nonce")
	if nonce == "" {
		fmt.Println("cc recv response /control/hello not ok, no nonce")
		return
	}

	session_key := common.CalSha256(cc.svcConfig.Token + nonce)
	req, err = http.NewRequest("GET", "/control/auth", nil)
	if err != nil {
		fmt.Println("cc create request /control/auth fail", err)
		return
	}
	req.Header.Set("session_key", session_key)

	err = req.Write(conn)
	if err != nil {
		fmt.Println("cc send request /control/auth fail", err)
		return
	}
	fmt.Println("cc send request /control/auth success")

	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		fmt.Println("cc recv response /control/auth fail", err)
		return
	}

	if resp.StatusCode != http.StatusOK { // 401-token不正确，拒绝访问
		fmt.Println("cc recv response /control/auth not ok", resp.StatusCode, session_key)
		return
	}

	cmd_chan, err_chan := cc.do_read_cmd(br)
	go cc.do_send_heartbeat_to_server(conn)

OUTER:
	for {
		select {
		case <-cc.cancelCtx.Done():
			fmt.Println("cc receive cancel")
			break OUTER

		case <-err_chan:
			fmt.Println("cc select error")
			needReconnect = true
			break OUTER

		case cmd := <-cmd_chan:
			if cmd == "datachannel" {
				fmt.Println("cc recv server cmd datachannel")
				args := RunDataChannelArgs{
					clientConfig: cc.clientConfig,
					svcConfig:    cc.svcConfig,
					sessionKey:   session_key,
				}

				go create_data_channel(cc.cancelCtx, args)
			} else { // "heartbeat"
				fmt.Println("cc recv server cmd heartbeat")
			}

		case <-time.After(120 * time.Second):
			fmt.Println("cc recv server heartbeat timeout")
			needReconnect = true
			break OUTER
		}
	}
}

func (cc *ControlChannel) do_read_cmd(br *bufio.Reader) (chan string, chan error) {
	cmd_chan := make(chan string, 1)
	err_chan := make(chan error, 1)

	go func() {
		for {
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				select {
				case <-cc.cancelCtx.Done():
					fmt.Println("cc recv server /control/cmd fail, cancelCtx.Done()")
				default:
					fmt.Println("cc recv server /control/cmd fail", err)
				}

				select {
				case <-cc.cancelCtx.Done():
					return
				case err_chan <- err:
				}

				return
			}
			select {
			case <-cc.cancelCtx.Done():
				return
			case cmd_chan <- resp.Header.Get("cmd"):
			}
		}
	}()
	return cmd_chan, err_chan
}

func (cc *ControlChannel) do_send_heartbeat_to_server(conn net.Conn) {
	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-time.After(110 * time.Second):
			fmt.Println("cc start send to server /contro/cmd heartbeat")
			go func() {
				resp := &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Header:     make(map[string][]string),
				}
				resp.Header.Set("cmd", "heartbeat")
				cc.err = common.ResponseWriteWithBuffered(resp, conn)
				if cc.err != nil {
					fmt.Println("cc send to server /control/cmd heartbeat fail", cc.err)
					cc.Cancel()
				}
			}()
		}
	}
}

func (cc *ControlChannel) Reconnect() {
	fmt.Println("cc need reconnect", cc.svcName)
	time.Sleep(3 * time.Second)
	cc.client.svc_chan <- cc.svcName
}

func create_data_channel(parentCtx context.Context, args RunDataChannelArgs) error {
	ctx, cancel := context.WithCancel(parentCtx)

	defer cancel()

	var conn *net.TCPConn
	var err error
	tcpAdr, _ := net.ResolveTCPAddr("tcp", args.clientConfig.RemoteAddr)

	for i := 0; i < 1; i++ {
		conn, err = net.DialTCP("tcp", nil, tcpAdr)
		if err != nil {
			fmt.Println("datachannel server connect fail", err)
			continue
		} else {
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
		err := conn.Close() // 关闭连接
		if err != nil {
			fmt.Println("datachannel conn.Close error", err)
		}
	}()

	myconn := common.NewMyTcpConn(conn)

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

	fmt.Println("recv response /data/hello ok", forwardType, respbuf, myconn.LocalAddr(), myconn.RemoteAddr())

	if forwardType == "tcp" {
		forward_data_channel_for_tcp(ctx, myconn, args.svcConfig.LocalAddr, string(forwardType))
	} else if forwardType == "udp" {
		forward_data_channel_for_udp(ctx, myconn, args.svcConfig.LocalAddr, string(forwardType))
	} else {
		fmt.Println("unknown forward type", forwardType)
	}

	return nil
}

func forward_data_channel_for_tcp(ctx context.Context, remoteConn *common.MyTcpConn, localAddr string, network string) {
	tcpAdr, _ := net.ResolveTCPAddr("tcp", localAddr)
	fmt.Println("forward_data_channel_for_tcp", tcpAdr, localAddr)
	clientConn, err := net.DialTCP(network, nil, tcpAdr)
	if err != nil {
		fmt.Println("connect localaddr fail", err)
		return
	}
	defer func() {
		fmt.Println("forward_data_channel_for_tcp clientConn.Close()")
		err := clientConn.Close()
		if err != nil {
			fmt.Println("forward_data_channel_for_tcp clientConn.Close error", err)
		}
	}()

	fmt.Println("forward_data_channel_for_tcp, clientConn.LocalAddr", clientConn.LocalAddr(), clientConn.RemoteAddr())

	err = common.CopyTcpConnection(ctx, clientConn, remoteConn)
	if err != nil {
		fmt.Println("CopyTcpConnection error", err)
	} else {
		fmt.Println("CopyTcpConnection success", err)
	}
}

func forward_data_channel_for_udp(ctx context.Context, remoteConn *common.MyTcpConn, localAddr string, network string) {
	laddr, err := net.ResolveUDPAddr(network, localAddr)
	if err != nil {
		fmt.Println("forward_data_channel_for_udp localAddr invalid", err)
		return
	}

	remotePacketChan := make(chan *common.UdpPacket)
	err_chan := make(chan error, 1)

	go func() {
		for {
			p := make([]byte, 8192)
			remoteAddr, n, err := remoteConn.ReadPacket(p)
			if err != nil {
				fmt.Println("forward_data_channel_for_udp ReadPacket error", err)
				go func() {
					select {
					case <-ctx.Done():
						return
					case err_chan <- err:
					}
				}()
				return
			}
			fmt.Println("forward_data_channel_for_udp remoteConn.ReadPacket", n, remoteAddr)

			select {
			case <-ctx.Done():
				return
			case remotePacketChan <- &common.UdpPacket{Payload: p[:n], Addr: remoteAddr}:
			}
		}
	}()

	mu := sync.Mutex{}
	udpConnMapping := make(map[string]*net.UDPConn)

label_for_one:
	for {
		select {
		case <-ctx.Done():
			break label_for_one

		case <-err_chan:
			break label_for_one

		case remotePacket := <-remotePacketChan:
			rAddrStr := remotePacket.Addr.String()
			mu.Lock()
			udpConn, ok := udpConnMapping[rAddrStr]
			mu.Unlock()
			if ok {
				go forward_packet_tcp_to_udp(udpConn, remotePacket, laddr)
			} else {
				randAddr, err := net.ResolveUDPAddr(network, "") // 随机端口
				if err != nil {
					fmt.Println("forward_data_channel_for_udp resolveudpaddr error", err)
					break label_for_one
				}
				udpConn, err := net.ListenUDP(network, randAddr) // todo 这里的监听地址，需要改成其他机器能访问的
				if err != nil {
					fmt.Println("forward_data_channel_for_udp ListenUDP fail", err)
					break label_for_one
				}
				fmt.Println("forward_data_channel_for_udp ListenUDP LocalAddr", udpConn.LocalAddr())
				mu.Lock()
				udpConnMapping[rAddrStr] = udpConn
				mu.Unlock()

				go forward_packet_udp_to_tcp(ctx, udpConn, remoteConn, remotePacket, localAddr)
				go forward_packet_tcp_to_udp(udpConn, remotePacket, laddr)
			}
		}
	}
}

func forward_packet_tcp_to_udp(udpConn *net.UDPConn, remotePacket *common.UdpPacket, laddr *net.UDPAddr) {
	n, err := udpConn.WriteToUDP(remotePacket.Payload, laddr)
	if err != nil {
		fmt.Println("forward_packet_tcp_to_udp udpConn.WriteToUDP error", err)
		return
	}
	fmt.Println("forward_packet_tcp_to_udp udpConn.WriteToUDP success", n)
}

func forward_packet_udp_to_tcp(ctx context.Context, udpConn *net.UDPConn, remoteConn *common.MyTcpConn, remotePacket *common.UdpPacket, localAddr string) {
	localUdpPacketChan := make(chan *common.UdpPacket, 32)

	err_chan := make(chan error, 1)

	go func() {
		for {
			p := make([]byte, 8192)
			n, addr, err := udpConn.ReadFromUDP(p)
			if err != nil {
				fmt.Println("forward_packet_udp_to_tcp clientUDPConn.ReadFrom error", err)
				go func() {
					select {
					case <-ctx.Done():
						return
					case err_chan <- err:
					}
				}()
				return
			}
			if addr.String() != localAddr {
				fmt.Println("forward_packet_udp_to_tcp addr not equal, continue", localAddr, addr.String())
				continue
			}

			select {
			case <-ctx.Done():
				return
			case localUdpPacketChan <- &common.UdpPacket{Payload: p[:n], Addr: addr}:
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case <-err_chan:
			return

		case localPacket := <-localUdpPacketChan:
			go func() {
				raddr, ok := remotePacket.Addr.(*common.Address)
				if !ok {
					fmt.Println("forward_packet_udp_to_tcp remotePacket.Addr is not common.Address")
					return
				}
				n, err := remoteConn.WritePacket(localPacket.Payload, raddr)
				if err != nil {
					fmt.Println("forward_packet_udp_to_tcp remote.WritePacket error", err)
					return
				}

				fmt.Println("forward_packet_udp_to_tcp remoteConn.WritePacket success", n)
			}()
		}
	}
}