package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
	"github.com/sethvargo/go-retry"
)

const (
	// udp 会话在这么久没有任何收发后回收，否则每个新的来源地址都会留下
	// 一个 socket 加一个 goroutine，对公网服务来说就是持续泄漏直到 fd 耗尽。
	udpSessionIdleTimeout = 60 * time.Second
	// 会话数上限，兜住突发的源地址扫描
	udpSessionMax = 512
)

// udpSession 是「某个远端来源地址」对应的本地 udp socket。
type udpSession struct {
	conn       *net.UDPConn
	lastActive atomic.Int64 // unixnano
}

func (s *udpSession) touch() {
	s.lastActive.Store(time.Now().UnixNano())
}

func (s *udpSession) idleFor() time.Duration {
	return time.Since(time.Unix(0, s.lastActive.Load()))
}

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

	udpConnMap      map[string]*udpSession
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

		udpConnMap: make(map[string]*udpSession),
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
		if errors.Is(err, common.ErrUnauthorized) {
			// 这是配置问题不是网络问题，重连解决不了，明确提示出来
			fmt.Println("cc 认证失败，请检查服务 [", cc.svcName, "] 的 token 是否与服务端一致")
		} else {
			fmt.Println("cc Do_control_channel_handshake fail", err)
		}
		needReconnect = true
		return
	}

	cmd_chan := make(chan string, 6)

	go cc.do_read_cmd(br, cmd_chan, cc.err_chan)
	go cc.do_send_heartbeat_to_server(conn, cc.err_chan)

	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()

OUTER:
	for {
		select {
		case <-cc.cancelCtx.Done():
			fmt.Println("cc receive cancel")
			break OUTER

		case e := <-cc.err_chan:
			fmt.Println("cc select error", e)
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
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(120 * time.Second)

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

	ticker := time.NewTicker(110 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-ticker.C:
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
	tcpAdr, err := net.ResolveTCPAddr("tcp", localAddr)
	if err != nil {
		fmt.Println("forward_data_channel_for_tcp local_addr invalid", localAddr, err)
		return
	}
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

	targetLocalAddr, err := net.ResolveUDPAddr(network, localAddr)
	if err != nil {
		fmt.Println("datachannel forward_data_channel_for_udp localAddr invalid", err)
		return
	}

	// 注意这里传的是 cc.cancelCtx 而不是本条数据通道的 ctx。
	// forward_udp_inbound 是 inboundPacketChan 唯一的消费者，而 once 只会触发一次；
	// 如果绑在第一条数据通道的 ctx 上，服务端例行回收掉那条通道时它就永久退出了，
	// 之后所有数据通道收到的包全都没人消费，udp 转发静默变成黑洞，
	// 而控制通道还活着不会触发重连。
	cc.once.Do(func() {
		fmt.Println("datachannel forward_data_channel_for_udp once.Do")
		go cc.forward_udp_inbound(cc.cancelCtx, targetLocalAddr, network)
	})

	local_err_chan := make(chan error, 3)
	go cc.read_packet_from_tcpconn(ctx, remoteConn, cc.inboundPacketChan, local_err_chan)
	go cc.write_packet_to_tcpconn(ctx, remoteConn, cc.outboundPacketChan, local_err_chan)

	select {
	case <-ctx.Done():
	case e := <-local_err_chan:
		fmt.Println("datachannel forward_data_channel_for_udp err_chan", e)
	}
}

func (cc *ControlChannel) read_packet_from_tcpconn(ctx context.Context, tcpConn *common.MyTcpConn, inboundChan chan *common.UdpPacket, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel read_packet_from_tcpconn end")
	}()

	for {
		p := make([]byte, common.MaxPacketSize)
		remoteAddr, n, err := tcpConn.ReadPacket(p)
		if err != nil {
			fmt.Println("datachannel read_packet_from_tcpconn tcpConn.ReadPacket error", err)
			common.SendError(ctx, err_chan, err)
			return
		}
		common.Debug("datachannel read_packet_from_tcpconn tcpConn.ReadPacket", n, "packet.Addr:", remoteAddr)

		select {
		case <-ctx.Done():
			return
		case inboundChan <- &common.UdpPacket{Payload: p[:n], Addr: remoteAddr}:
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
			common.Debug("datachannel write_packet_to_tcpconn tcpConn.WritePacket success", n, "packet.Addr:", packet.Addr)
		}
	}
}

func (cc *ControlChannel) forward_udp_inbound(ctx context.Context, targetLocalAddr *net.UDPAddr, network string) {
	defer func() {
		fmt.Println("datachannel forward_udp_inbound end")
		cc.closeAllUdpSessions()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case inboundPacket := <-cc.inboundPacketChan:
			key := inboundPacket.Addr.String()

			sess := cc.getOrCreateUdpSession(ctx, key, inboundPacket.Addr, network, targetLocalAddr)
			if sess == nil {
				continue
			}
			sess.touch()

			// 单个包发不出去只影响这一个来源，绝不能 return：
			// 这个循环是所有 udp 入方向流量的唯一出口，退出即全局黑洞。
			n, err := sess.conn.WriteToUDP(inboundPacket.Payload, targetLocalAddr)
			if err != nil {
				fmt.Println("datachannel forward_udp_inbound udpConn.WriteToUDP error", err)
				cc.removeUdpSession(key, sess)
				continue
			}
			common.Debug("datachannel forward_udp_inbound udpConn.WriteToUDP success", n, "inboundPacket.Addr:", inboundPacket.Addr)
		}
	}
}

func (cc *ControlChannel) getOrCreateUdpSession(ctx context.Context, key string, remoteAddr *common.Address, network string, targetLocalAddr *net.UDPAddr) *udpSession {
	cc.udpConnMapMutex.Lock()
	defer cc.udpConnMapMutex.Unlock()

	if sess, ok := cc.udpConnMap[key]; ok {
		return sess
	}

	if len(cc.udpConnMap) >= udpSessionMax {
		fmt.Println("datachannel forward_udp_inbound udp 会话数已达上限，丢弃数据包", key)
		return nil
	}

	fmt.Println("datachannel forward_udp_inbound udpConn not exist, create new udpConn, rAddrStr:", key)
	udpConn, err := net.ListenUDP(network, nil) // 随机端口
	if err != nil {
		fmt.Println("datachannel forward_udp_inbound ListenUDP fail", err)
		return nil
	}
	fmt.Println("datachannel forward_udp_inbound ListenUDP LocalAddr", udpConn.LocalAddr())

	sess := &udpSession{conn: udpConn}
	sess.touch()
	cc.udpConnMap[key] = sess

	go cc.forward_udp_outbound(ctx, sess, key, remoteAddr, targetLocalAddr)

	return sess
}

// removeUdpSession 摘除并关闭会话。
// 比对指针是必需的：同一个来源地址可能已经建了新会话，
// 只按 key 删会把新会话误删掉。
func (cc *ControlChannel) removeUdpSession(key string, sess *udpSession) {
	cc.udpConnMapMutex.Lock()
	if cur, ok := cc.udpConnMap[key]; ok && cur == sess {
		delete(cc.udpConnMap, key)
	}
	cc.udpConnMapMutex.Unlock()

	if err := sess.conn.Close(); err != nil {
		common.Debug("datachannel removeUdpSession Close error", err)
	}
}

func (cc *ControlChannel) closeAllUdpSessions() {
	cc.udpConnMapMutex.Lock()
	list := make([]*udpSession, 0, len(cc.udpConnMap))
	for key, sess := range cc.udpConnMap {
		list = append(list, sess)
		delete(cc.udpConnMap, key)
	}
	cc.udpConnMapMutex.Unlock()

	for _, sess := range list {
		sess.conn.Close()
	}
}

func (cc *ControlChannel) forward_udp_outbound(ctx context.Context, sess *udpSession, key string, remoteAddr *common.Address, targetLocalAddr *net.UDPAddr) {
	fmt.Println("datachannel forward_udp_outbound start, remoteAddr:", key, "localAddr:", targetLocalAddr.String())

	defer func() {
		fmt.Println("datachannel forward_udp_outbound end, start cleanup udpConn, remoteAddr:", key)
		cc.removeUdpSession(key, sess)
	}()

	for {
		// 读超时兼作空闲回收的计时器：到点后再看这个会话是不是真的没流量了。
		if err := sess.conn.SetReadDeadline(time.Now().Add(udpSessionIdleTimeout)); err != nil {
			return
		}

		p := make([]byte, common.MaxPacketSize)
		n, addr, err := sess.conn.ReadFromUDP(p)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				// 入方向还在用这个会话就接着等，否则回收掉
				if sess.idleFor() < udpSessionIdleTimeout {
					continue
				}
				fmt.Println("datachannel forward_udp_outbound udp 会话空闲超时，回收", key)
				return
			}
			common.Debug("datachannel forward_udp_outbound clientUDPConn.ReadFrom error", err)
			return
		}

		sess.touch()

		common.Debug("datachannel forward_udp_outbound udpConn.ReadFromUDP success", n, "from:", addr)

		if addr.String() != targetLocalAddr.String() {
			common.Debug("datachannel forward_udp_outbound addr not equal, packet.Addr", addr.String(), "expect:", targetLocalAddr)
			continue
		}

		select {
		case <-ctx.Done():
			return
		case cc.outboundPacketChan <- &common.UdpPacket{Payload: p[:n], Addr: remoteAddr}:
		}
	}
}
