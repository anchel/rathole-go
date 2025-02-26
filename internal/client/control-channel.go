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

	inboundPacketChan  chan *common.UdpPacket // for udp
	outboundPacketChan chan *common.UdpPacket // for udp

	once     sync.Once // only for udp
	err_chan chan error

	udpConnMap      map[string]*net.UDPConn
	udpConnMapMutex sync.Mutex
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

		inboundPacketChan:  make(chan *common.UdpPacket, 32),
		outboundPacketChan: make(chan *common.UdpPacket, 32),

		err_chan: make(chan error, 3),

		udpConnMap: make(map[string]*net.UDPConn),
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
			fmt.Println("cc need reconnect")
			ciChan <- &ComunicationItem{method: "reconnect", payload: cc.svcName}
		}
	}()

	defer cc.Cancel()

	var conn net.Conn

	b := retry.NewFibonacci(1 * time.Second)
	b = retry.WithMaxRetries(3, b)
	err := retry.Do(cc.cancelCtx, b, func(ctx context.Context) error {
		timeout := 5 * time.Second // 设置超时时间为 5 秒
		con, err := net.DialTimeout("tcp", ctx.Value(common.ContextKey("remoteAddr")).(string), timeout)
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
		needReconnect = true
		return
	}

	cmd_chan := make(chan string, 6)

	go cc.do_read_cmd(br, cmd_chan, cc.err_chan)
	go cc.do_send_heartbeat_to_server(conn, cc.err_chan)

	timer := time.NewTimer(120 * time.Second)

OUTER:
	for {
		if !timer.Stop() {
			<-timer.C
		}
		timer.Reset(120 * time.Second)

		select {
		case <-cc.cancelCtx.Done():
			fmt.Println("cc receive cancel")
			break OUTER

		case <-cc.err_chan:
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

				go cc.create_data_channel(cc.cancelCtx, args)
			} else { // "heartbeat"
				fmt.Println("cc recv server cmd heartbeat")
			}

		case <-timer.C:
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
				Proto:      "HTTP/1.1",
				ProtoMajor: 1,
				ProtoMinor: 1,
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

func (cc *ControlChannel) create_data_channel(parentCtx context.Context, args RunDataChannelArgs) error {
	defer func() {
		fmt.Println("datachannel create_data_channel end")
	}()

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	var conn *net.TCPConn
	var err error

	b := retry.NewFibonacci(1 * time.Second)
	b = retry.WithMaxRetries(3, b)
	err = retry.Do(ctx, b, func(innerCtx context.Context) error {
		timeout := 5 * time.Second // 设置超时时间为 5 秒
		con, err := net.DialTimeout("tcp", ctx.Value(common.ContextKey("remoteAddr")).(string), timeout)
		if err != nil {
			fmt.Println("datachannel connect server fail", err)
			return retry.RetryableError(err)
		}
		conn = con.(*net.TCPConn)
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
		cc.forward_data_channel_for_tcp(ctx, myconn, args.svcConfig.LocalAddr, forwardType)
	} else if forwardType == "udp" {
		cc.forward_data_channel_for_udp(ctx, myconn, args.svcConfig.LocalAddr, forwardType)
	} else {
		fmt.Println("unknown forward type", forwardType)
	}

	return nil
}

func (cc *ControlChannel) forward_data_channel_for_tcp(ctx context.Context, remoteConn *common.MyTcpConn, localAddr string, network string) {
	tcpAdr, _ := net.ResolveTCPAddr("tcp", localAddr)
	fmt.Println("forward_data_channel_for_tcp", tcpAdr, localAddr)

	timeout := 5 * time.Second // 设置超时时间为 5 秒
	conn, err := net.DialTimeout(network, tcpAdr.String(), timeout)

	if err != nil {
		fmt.Println("connect localaddr fail", err)
		return
	}

	clientConn := conn.(*net.TCPConn)
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

func (cc *ControlChannel) forward_data_channel_for_udp(ctx context.Context, remoteConn *common.MyTcpConn, localAddr string, network string) {
	defer func() {
		fmt.Println("datachannel forward_data_channel_for_udp end")
	}()

	var err error

	targetLocalAddr, err := net.ResolveUDPAddr(network, localAddr)
	if err != nil {
		fmt.Println("datachannel forward_data_channel_for_udp localAddr invalid", err)
		return
	}

	cc.once.Do(func() {
		fmt.Println("datachannel forward_data_channel_for_udp once.Do")
		go cc.forward_udp_inbound(ctx, targetLocalAddr, network, cc.err_chan)
	})

	local_err_chan := make(chan error, 3)
	go cc.read_packet_from_tcpconn(ctx, remoteConn, cc.inboundPacketChan, local_err_chan)
	go cc.write_packet_to_tcpconn(ctx, remoteConn, cc.outboundPacketChan, local_err_chan)

	for {
		select {
		case <-ctx.Done():
			return
		case e := <-local_err_chan:
			fmt.Println("datachannel forward_data_channel_for_udp err_chan", e)
			return
		}
	}
}

func (cc *ControlChannel) read_packet_from_tcpconn(ctx context.Context, tcpConn *common.MyTcpConn, inboundChan chan *common.UdpPacket, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel read_packet_from_tcpconn end")
	}()

	localCh := make(chan *common.UdpPacket, 32)

	go func() {
		defer func() {
			fmt.Println("datachannel read_packet_from_tcpconn tcpConn.ReadPacket end")
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
			case localCh <- &common.UdpPacket{Payload: p[:n], Addr: remoteAddr}:
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case packet := <-localCh:
			select {
			case <-ctx.Done():
				return
			case inboundChan <- packet:
			}
		}
	}
}

func (cc *ControlChannel) write_packet_to_tcpconn(ctx context.Context, tcpConn *common.MyTcpConn, outboundChan chan *common.UdpPacket, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel write_packet_to_tcpconn end")
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case packet := <-outboundChan:
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

func (cc *ControlChannel) forward_udp_inbound(ctx context.Context, targetLocalAddr *net.UDPAddr, network string, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel forward_udp_inbound end")
	}()

	var err error

	for {
		select {
		case <-ctx.Done():
			return
		case inboundPacket := <-cc.inboundPacketChan:
			rAddrStr := inboundPacket.Addr.String()

			cc.udpConnMapMutex.Lock()

			udpConn, ok := cc.udpConnMap[rAddrStr]

			if !ok {
				fmt.Println("datachannel forward_udp_inbound udpConn not exist, create new udpConn, rAddrStr:", rAddrStr)
				var randAddr *net.UDPAddr
				randAddr, err = net.ResolveUDPAddr(network, "") // 随机端口
				if err != nil {
					fmt.Println("datachannel forward_udp_inbound resolve udp addr error", err)
					common.SendError(ctx, err_chan, err)
					return
				}
				udpConn, err = net.ListenUDP(network, randAddr)
				if err != nil {
					fmt.Println("datachannel forward_udp_inbound ListenUDP fail", err)
					common.SendError(ctx, err_chan, err)
					return
				}
				fmt.Println("datachannel forward_udp_inbound ListenUDP LocalAddr", udpConn.LocalAddr())

				cc.udpConnMap[rAddrStr] = udpConn

				go cc.forward_udp_outbound(ctx, udpConn, cc.outboundPacketChan, inboundPacket.Addr, targetLocalAddr)
			}

			cc.udpConnMapMutex.Unlock()

			n, err := udpConn.WriteToUDP(inboundPacket.Payload, targetLocalAddr)
			if err != nil {
				fmt.Println("datachannel forward_udp_inbound udpConn.WriteToUDP error", err)
				common.SendError(ctx, err_chan, err)
				return
			}
			fmt.Println("datachannel forward_udp_inbound udpConn.WriteToUDP success", n, "inboundPacket.Addr:", inboundPacket.Addr)
		}
	}
}

func (cc *ControlChannel) forward_udp_outbound(ctx context.Context, udpConn *net.UDPConn, outboundPacketChan chan *common.UdpPacket, remoteAddr *common.Address, targetLocalAddr *net.UDPAddr) {
	fmt.Println("datachannel forward_udp_outbound start, remoteAddr:", remoteAddr.String(), "localAddr:", targetLocalAddr.String())

	defer func() {
		fmt.Println("datachannel forward_udp_outbound end, start cleanup udpConn, remoteAddr:", remoteAddr.String(), "localAddr:", targetLocalAddr.String())
		// cleanup udpConn
		err := udpConn.Close()
		if err != nil {
			fmt.Println("datachannel forward_udp_outbound end udpConn.Close error", err)
		}
		cc.udpConnMapMutex.Lock()
		delete(cc.udpConnMap, remoteAddr.String())
		cc.udpConnMapMutex.Unlock()
	}()

	localCh := make(chan *common.UdpPacket, 32)

	go func() {
		defer func() {
			fmt.Println("datachannel forward_udp_outbound udpConn.ReadFromUDP end")
		}()

		for {
			p := make([]byte, 8192)
			n, addr, err := udpConn.ReadFromUDP(p)
			if err != nil {
				fmt.Println("datachannel forward_udp_outbound clientUDPConn.ReadFrom error", err)
				return
			}

			fmt.Println("datachannel forward_udp_outbound udpConn.ReadFromUDP success", n, "from:", addr)

			if addr.String() != targetLocalAddr.String() {
				fmt.Println("datachannel forward_udp_outbound addr not equal, packet.Addr", addr.String(), "expect:", targetLocalAddr)
				continue
			}

			select {
			case <-ctx.Done():
				return
			case localCh <- &common.UdpPacket{Payload: p[:n], Addr: remoteAddr}:
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case packet := <-localCh:
			select {
			case <-ctx.Done():
				return
			case outboundPacketChan <- packet:
			}
		}
	}
}
