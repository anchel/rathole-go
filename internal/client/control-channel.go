package client

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
	"github.com/sethvargo/go-retry"
)

type ControlChannel struct {
	svcName string

	svcConfig *config.ClientServiceConfig

	cancelCtx context.Context
	cancel    context.CancelFunc

	muCancel sync.Mutex
	canceled bool
}

type RunDataChannelArgs struct {
	svcConfig  *config.ClientServiceConfig
	sessionKey string
}

func NewControlChannel(clientCtx *context.Context, svcName string, svcConfig *config.ClientServiceConfig) *ControlChannel {
	cancelCtx, cancel := context.WithCancel(*clientCtx)

	return &ControlChannel{
		svcName:   svcName,
		svcConfig: svcConfig,
		cancelCtx: cancelCtx,
		cancel:    cancel,
	}
}

func (cc *ControlChannel) Close() {
	fmt.Println("cc close")
	cc.Cancel()
}

func (cc *ControlChannel) Cancel() {
	cc.muCancel.Lock()
	defer cc.muCancel.Unlock()
	if !cc.canceled {
		cc.canceled = true
		cc.cancel()
	}
}

func (cc *ControlChannel) Run(ciChan chan *ComunicationItem) {
	defer func() {
		fmt.Println("cc Run end")
	}()

	if cc.canceled {
		return
	}

	needReconnect := false // 是否重连
	defer func() {
		if needReconnect {
			ciChan <- &ComunicationItem{method: "reconnect", payload: cc.svcName}
		}
	}()

	defer cc.Cancel()

	var conn net.Conn

	b := retry.NewFibonacci(1 * time.Second)
	b = retry.WithMaxRetries(3, b)
	err := retry.Do(cc.cancelCtx, b, func(ctx context.Context) error {
		con, err := net.Dial("tcp", ctx.Value(ContextKey("remoteAddr")).(string))
		if err != nil {
			fmt.Println("cc connect server fail", err)
			return retry.RetryableError(err)
		}
		conn = con
		return nil
	})
	if err != nil {
		fmt.Println("cc server retry.Do fail", err)
		needReconnect = true
		return
	}

	if conn == nil {
		fmt.Println("cc conn is nil")
		return
	}

	defer func() {
		fmt.Println("cc conn.Close")
		conn.Close()
	}()

	br := bufio.NewReader(conn)

	session_key, err := common.Do_control_channel_handshake(conn, br, cc.svcName, cc.svcConfig.Token)
	if err != nil {
		fmt.Println("cc Do_control_channel_handshake fail", err)
		return
	}

	cmd_chan := make(chan string, 6)
	err_chan := make(chan error, 3)

	go cc.do_read_cmd(br, cmd_chan, err_chan)
	go cc.do_send_heartbeat_to_server(conn, err_chan)

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
					svcConfig:  cc.svcConfig,
					sessionKey: session_key,
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

func (cc *ControlChannel) do_read_cmd(br *bufio.Reader, cmd_chan chan string, err_chan chan error) {

	defer func() {
		fmt.Println("cc do_read_cmd for end")
	}()

	for {
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			select {
			case <-cc.cancelCtx.Done():
				fmt.Println("cc recv server /control/cmd fail, cancelCtx.Done()")
			default:
				fmt.Println("cc recv server /control/cmd fail", err)
			}
			common.SendError(cc.cancelCtx, err_chan, err)
			return
		}
		select {
		case <-cc.cancelCtx.Done():
			return
		case cmd_chan <- resp.Header.Get("cmd"):
		}
	}
}

func (cc *ControlChannel) do_send_heartbeat_to_server(conn net.Conn, err_chan chan error) {
	defer func() {
		fmt.Println("cc do_send_heartbeat_to_server end")
	}()

	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-time.After(110 * time.Second):
			fmt.Println("cc start send to server /contro/cmd heartbeat")
			resp := &http.Response{
				Status:     "200 OK",
				StatusCode: http.StatusOK,
				Header:     make(map[string][]string),
			}
			resp.Header.Set("cmd", "heartbeat")
			err := common.ResponseWriteWithBuffered(resp, conn)
			if err != nil {
				fmt.Println("cc send to server /control/cmd heartbeat fail", err)
				common.SendError(cc.cancelCtx, err_chan, err)
				return
			}
		}
	}
}

func create_data_channel(parentCtx context.Context, args RunDataChannelArgs) error {
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	var conn *net.TCPConn
	var err error
	tcpAdr, _ := net.ResolveTCPAddr("tcp", ctx.Value(ContextKey("remoteAddr")).(string))

	b := retry.NewFibonacci(1 * time.Second)
	b = retry.WithMaxRetries(3, b)
	err = retry.Do(ctx, b, func(innerCtx context.Context) error {
		con, err := net.DialTCP("tcp", nil, tcpAdr)
		if err != nil {
			fmt.Println("datachannel connect server fail", err)
			return retry.RetryableError(err)
		}
		conn = con
		return nil
	})
	if err != nil {
		fmt.Println("datachannel retry.Do fail", err)
		return err
	}

	if conn == nil {
		fmt.Println("datachannel conn is nil ")
		return err
	}

	fmt.Println("datachannel connected to server, LocalAddr:", conn.LocalAddr(), "RemoteAddr:", conn.RemoteAddr())

	defer func() {
		fmt.Println("datachannel conn.Close")
		err := conn.Close() // 关闭连接
		if err != nil {
			fmt.Println("datachannel conn.Close error", err)
		}
	}()

	myconn := common.NewMyTcpConn(conn)

	forwardType, err := common.Do_data_channel_handshake(myconn, args.sessionKey, args.svcConfig.Type)
	if err != nil {
		fmt.Println("datachannel do handshake fail", err)
		return err
	}

	fmt.Println("datachannel do handshake ok", forwardType)

	if forwardType == "tcp" {
		forward_data_channel_for_tcp(ctx, myconn, args.svcConfig.LocalAddr, forwardType)
	} else if forwardType == "udp" {
		forward_data_channel_for_udp(ctx, myconn, args.svcConfig.LocalAddr, forwardType)
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
	defer func() {
		fmt.Println("datachannel forward_data_channel_for_udp end")
	}()

	laddr, err := net.ResolveUDPAddr(network, localAddr)
	if err != nil {
		fmt.Println("datachannel forward_data_channel_for_udp localAddr invalid", err)
		return
	}

	incomingPacketChan := make(chan *common.UdpPacket, 32)  // tcp 连接收到的包
	outcomingPacketChan := make(chan *common.UdpPacket, 32) // 需要通过tcp连接发送的包
	err_chan := make(chan error, 3)

	go read_packet_from_tcpconn(ctx, remoteConn, incomingPacketChan, err_chan)
	go write_packet_to_tcpconn(ctx, remoteConn, outcomingPacketChan, err_chan)

	mu := sync.Mutex{}
	mapping := make(map[string]chan *common.UdpPacket)

label_for_outer:
	for {
		select {
		case <-ctx.Done():
			break label_for_outer

		case <-err_chan:
			break label_for_outer

		case remotePacket := <-incomingPacketChan:
			rAddrStr := remotePacket.Addr.String()
			mu.Lock()
			_, ok := mapping[rAddrStr]
			mu.Unlock()

			if !ok {
				inboundChan := make(chan *common.UdpPacket, 32)
				randAddr, err := net.ResolveUDPAddr(network, "") // 随机端口
				if err != nil {
					fmt.Println("datachannel forward_data_channel_for_udp resolveudpaddr error", err)
					break label_for_outer
				}
				udpConn, err := net.ListenUDP(network, randAddr) // todo 这里的监听地址，需要改成其他机器能访问的
				if err != nil {
					fmt.Println("datachannel forward_data_channel_for_udp ListenUDP fail", err)
					break label_for_outer
				}
				fmt.Println("datachannel forward_data_channel_for_udp ListenUDP LocalAddr", udpConn.LocalAddr())
				mu.Lock()
				mapping[rAddrStr] = inboundChan
				mu.Unlock()
				go forward_udp_in_and_out(ctx, udpConn, inboundChan, outcomingPacketChan, err_chan, remotePacket.Addr, laddr)
			}
			mu.Lock()
			inboundPacketChan, ok := mapping[rAddrStr]
			mu.Unlock()
			if ok {
				select {
				case <-ctx.Done():
					break label_for_outer
				case inboundPacketChan <- remotePacket:
				}
			}
		}
	}
}

func read_packet_from_tcpconn(ctx context.Context, tcpConn *common.MyTcpConn, inboundChan chan *common.UdpPacket, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel read_packet_from_tcpconn end")
	}()

	for {
		p := make([]byte, 8192)
		remoteAddr, n, err := tcpConn.ReadPacket(p)
		if err != nil {
			fmt.Println("datachannel read_packet_from_tcpconn tcpConn.ReadPacket error", err)
			common.SendError(ctx, err_chan, err)
			return
		}
		fmt.Println("datachannel read_packet_from_tcpconn tcpConn.ReadPacket", n, "packet.Addr:", remoteAddr)

		select {
		case <-ctx.Done():
			return
		case inboundChan <- &common.UdpPacket{Payload: p[:n], Addr: remoteAddr}:
		}
	}
}

func write_packet_to_tcpconn(ctx context.Context, tcpConn *common.MyTcpConn, outcomingPacketChan chan *common.UdpPacket, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel write_packet_to_tcpconn end")
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case packet := <-outcomingPacketChan:
			n, err := tcpConn.WritePacket(packet.Payload, packet.Addr)
			if err != nil {
				fmt.Println("datachannel write_packet_to_tcpconn tcpConn.WritePacket error", err)
				common.SendError(ctx, err_chan, err)
				return
			}
			fmt.Println("datachannel write_packet_to_tcpconn tcpConn.WritePacket success", n, "packet.Addr:", packet.Addr)
		}
	}
}

func forward_udp_in_and_out(ctx context.Context, udpConn *net.UDPConn, inboundChan chan *common.UdpPacket, outboundChan chan *common.UdpPacket, err_chan chan error, remoteAddr *common.Address, localAddr *net.UDPAddr) {
	defer func() {
		fmt.Println("datachannel forward_udp_in_and_out end")
	}()

	local_err_chan := make(chan error, 3)

	go func() {
		defer func() {
			fmt.Println("datachannel forward_udp_in_and_out clientUDPConn.ReadFrom end")
		}()

		for {
			p := make([]byte, 8192)
			n, addr, err := udpConn.ReadFromUDP(p)
			if err != nil {
				fmt.Println("datachannel forward_udp_in_and_out clientUDPConn.ReadFrom error", err)
				common.SendError(ctx, local_err_chan, err)
				return
			}

			fmt.Println("datachannel forward_udp_in_and_out udpConn.ReadFromUDP success", n, "from:", addr)

			if addr.String() != localAddr.String() {
				fmt.Println("datachannel forward_udp_in_and_out addr not equal, packet.Addr", addr.String(), "expect:", localAddr)
				continue
			}

			select {
			case <-ctx.Done():
				return
			case outboundChan <- &common.UdpPacket{Payload: p[:n], Addr: remoteAddr}:
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case err := <-local_err_chan:
			common.SendError(ctx, err_chan, err)
			return

		case packet := <-inboundChan:
			n, err := udpConn.WriteToUDP(packet.Payload, localAddr)
			if err != nil {
				fmt.Println("datachannel forward_udp_in_and_out udpConn.WriteToUDP error", err)
				common.SendError(ctx, err_chan, err)
				return
			}
			fmt.Println("datachannel forward_udp_in_and_out udpConn.WriteToUDP success", n, "packet.Addr:", packet.Addr)
		}
	}
}
