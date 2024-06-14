package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
)

type ControlChannel struct {
	session_key  string
	clientConfig *config.ServerConfig
	service      *Service
	s            *Server

	connection *net.TCPConn
	reader     *bufio.Reader
	data_chan  chan net.Conn

	cancelCtx context.Context
	cancel    context.CancelFunc
	err       error

	muDone   sync.Mutex
	canceled bool
}

func NewControlChannel(parentCtx context.Context, session_key string, conf *config.ServerConfig, service *Service, s *Server, conn *net.TCPConn, reader *bufio.Reader) *ControlChannel {
	ctx, cancel := context.WithCancel(parentCtx)
	cc := &ControlChannel{
		session_key:  session_key,
		clientConfig: conf,
		service:      service,
		s:            s,

		connection: conn,
		reader:     reader,
		data_chan:  make(chan net.Conn, 6),

		cancelCtx: ctx,
		cancel:    cancel,
	}

	return cc
}

func (cc *ControlChannel) Close() {
	fmt.Println("ControlCancel Close")
	cc.err = errors.New("server cc close")
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
	defer cc.Cancel()

	data_req_chan := make(chan bool, 6)

	if cc.service.svcType == config.TCP {
		go run_tcp_loop(cc, data_req_chan)
	} else {
		go run_udp_loop(cc, data_req_chan)
	}

	cmd_chan, err_chan := cc.do_read_cmd()

	go cc.do_send_heartbeat_to_client()
	go cc.do_send_create_datachannel(data_req_chan)

label_for:
	for {
		if cc.err != nil {
			fmt.Println("cc for loop: cc.err != nil, break")
			break
		}
		select {
		case <-cc.cancelCtx.Done():
			fmt.Println("cc receive cancelCtx.Done()")
			break label_for

		case <-err_chan:
			fmt.Println("cc <-err_chan error")
			break label_for

		case cmd := <-cmd_chan:
			if cmd == "heartbeat" {
				fmt.Println("cc recv client heartbeat")
			} else {
				fmt.Println("cc recv client other cmd", cmd)
			}

		case <-time.After(120 * time.Second):
			fmt.Println("cc recv client heartbeat timeout")
			break label_for
		}
	}

	fmt.Println("cc Run Over")
}

func (cc *ControlChannel) do_read_cmd() (chan string, chan error) {

	cmd_chan := make(chan string, 1)
	err_chan := make(chan error, 1)

	go func() {
		for {
			resp, err := http.ReadResponse(cc.reader, nil)
			if err != nil {
				select {
				case <-cc.cancelCtx.Done():
					fmt.Println("cc recv client /control/cmd fail, cancelCtx.Done()")
				default:
					fmt.Println("cc recv client /control/cmd fail", err)
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

func (cc *ControlChannel) do_send_create_datachannel(data_req_chan chan bool) {
	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-data_req_chan:
			fmt.Println("cc start send to client /contro/cmd datachannel")
			go func() {
				resp := &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Header:     make(map[string][]string),
				}
				resp.Header.Set("cmd", "datachannel")
				cc.err = common.ResponseWriteWithBuffered(resp, cc.connection)
				if cc.err != nil {
					fmt.Println("cc send to client /control/cmd datachannel fail", cc.err)
					cc.Cancel()
				}
			}()
		}
	}
}

func (cc *ControlChannel) do_send_heartbeat_to_client() {
	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-time.After(110 * time.Second):
			fmt.Println("cc start send to client /contro/cmd heartbeat")
			if cc.err != nil {
				continue
			}
			go func() {
				resp := &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Header:     make(map[string][]string),
				}
				resp.Header.Set("cmd", "heartbeat")
				cc.err = common.ResponseWriteWithBuffered(resp, cc.connection)
				if cc.err != nil {
					fmt.Println("cc send to client /control/cmd heartbeat fail", cc.err)
					cc.Cancel()
				}
			}()
		}

	}
}

func run_tcp_loop(cc *ControlChannel, data_req_chan chan<- bool) {
	if cc.err != nil {
		fmt.Println("cc.err != nil, run_tcp_loop stop")
		return
	}
	tcpAddr, _ := net.ResolveTCPAddr("tcp", cc.service.bind_addr)
	l, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		fmt.Println("service listen fail", err)
		return
	}
	go func() {
		for {
			remoteConn, err := l.AcceptTCP()
			if err != nil {
				select {
				case <-cc.cancelCtx.Done():
					fmt.Println("service accept fail, because of cancelCtx.Done()", err)
				default:
					fmt.Println("service accept fail", err)
				}
				return
			}
			go forward_tcp_connection(cc, remoteConn, data_req_chan)
		}
	}()

	<-cc.cancelCtx.Done()
	fmt.Println("run_tcp_loop starting to finish")
	err = l.Close()
	if err != nil {
		fmt.Println("run_tcp_loop finish error", err)
	}
}

func forward_tcp_connection(cc *ControlChannel, remoteConn *net.TCPConn, data_req_chan chan<- bool) {
	// defer remoteConn.Close()

	fmt.Println("forward_tcp_connection", remoteConn.RemoteAddr())

	// 发送命令，指示客户端主动连接服务器
	go func() {
		select {
		case <-cc.cancelCtx.Done():
		case data_req_chan <- true:
		}
	}()

	var clientConn net.Conn
	select {
	case <-cc.cancelCtx.Done():
		return
	case clientConn = <-cc.data_chan:
	}

	clientTCPConn, ok := clientConn.(*common.MyTcpConn)
	if !ok {
		fmt.Println("forward_tcp_connection 获取的客户的连接不是tcp连接")
		return
	}

	fmt.Println("成功取得客户的连接", clientConn.RemoteAddr())
	defer clientConn.Close()

	err := common.CopyTcpConnection(cc.cancelCtx, clientTCPConn, remoteConn)
	if err != nil {
		fmt.Println("CopyTcpConnection error", err)
	} else {
		fmt.Println("CopyTcpConnection success", err)
	}
}

func run_udp_loop(cc *ControlChannel, data_req_chan chan<- bool) {
	if cc.err != nil {
		fmt.Println("cc.err != nil, run_tcp_loop stop")
		return
	}

	network := string(cc.service.svcType)

	laddr, err := net.ResolveUDPAddr(network, cc.service.bind_addr)
	if err != nil {
		fmt.Println("run_udp_loop laddr error", err)
		return
	}

	localUDPConn, err := net.ListenUDP(network, laddr)
	if err != nil {
		fmt.Println("run_udp_loop ListenUDP error", err)
		return
	}

	fmt.Println("run_udp_loop localUDPConn.LocalAddr", localUDPConn.LocalAddr())

	defer func() {
		fmt.Println("run_udp_loop localUDPConn Close")
		err := localUDPConn.Close()
		if err != nil {
			fmt.Println("run_udp_loop localUDPConn Close error", err)
		}
	}()

	incomingPacketChan := make(chan *common.UdpPacket, 32)  // udp 接收到的包
	outcomingPacketChan := make(chan *common.UdpPacket, 32) // tcp 接收到的包
	err_chan := make(chan error, 3)

	go forward_udp_in_and_out(cc, localUDPConn, incomingPacketChan, outcomingPacketChan, err_chan)

	// 目标：只和客户端建立单个连接，然后转发数据。后续可考虑支持多个连接

	var clientTCPConn *common.MyTcpConn
	var inboundUdpPacket *common.UdpPacket

	var tmpClientTCPConnChan chan *common.MyTcpConn

	defer func() {
		if clientTCPConn != nil {
			err := clientTCPConn.Close()
			if err != nil {
				fmt.Println("run_udp_loop clientTCPConn.Close error", err)
			}
		}
	}()

label_for_second:
	for {
		var tmpInboundUdpPacketChan chan *common.UdpPacket

		if tmpClientTCPConnChan != nil { // 正在获取客户端连接，此时应该暂停处理udp包
			tmpInboundUdpPacketChan = nil
		} else {
			if clientTCPConn != nil { // 如果客户端连接不为空，则也应该暂停处理，因为客户端连接会自动处理了
				tmpInboundUdpPacketChan = nil
			} else {
				tmpInboundUdpPacketChan = incomingPacketChan
			}
		}

		select {
		case <-cc.cancelCtx.Done():
			break label_for_second

		case <-err_chan:
			break label_for_second

		case ctc := <-tmpClientTCPConnChan:
			fmt.Println("run_udp_loop <-tmpClientTCPConnChan, LocalAddr:", ctc.LocalAddr(), "RemoteAddr:", ctc.RemoteAddr())
			clientTCPConn = ctc
			tmpClientTCPConnChan = nil
			var tmpUdpPacket *common.UdpPacket
			if inboundUdpPacket != nil {
				up := *inboundUdpPacket
				up2 := up // copy
				tmpUdpPacket = &up2
				inboundUdpPacket = nil
			}
			go write_packet_to_tcpconn(cc, clientTCPConn, incomingPacketChan, tmpUdpPacket, err_chan)
			go read_packet_from_tcpconn(cc, clientTCPConn, outcomingPacketChan, err_chan)

		case inboundUdpPacket = <-tmpInboundUdpPacketChan:
			fmt.Println("run_udp_loop <-tmpInboundUdpPacketChan")
			if clientTCPConn == nil {
				fmt.Println("run_udp_loop clientTCPConn == nil, send to client create datachannel")
				tmpClientTCPConnChan = make(chan *common.MyTcpConn)
				go func() {
					select {
					case <-cc.cancelCtx.Done():
						return
					case data_req_chan <- true:
					}

					var tmpConn *common.MyTcpConn
					var ok bool
					select {
					case <-cc.cancelCtx.Done():
						return
					case conn := <-cc.data_chan:
						tmpConn, ok = conn.(*common.MyTcpConn)
						if !ok {
							fmt.Println("run_udp_loop 获取的客户的连接不是tcp连接")
							return
						}
					}

					select {
					case <-cc.cancelCtx.Done():
						return
					case tmpClientTCPConnChan <- tmpConn:
					}
				}()
			}
		}
	}

	fmt.Println("run_udp_loop starting to finish")
}

func forward_udp_in_and_out(cc *ControlChannel, udpConn *net.UDPConn, inboundChan chan *common.UdpPacket, outboundChan chan *common.UdpPacket, err_chan chan error) {
	network := string(cc.service.svcType)

	go func() {
		for {
			payload := make([]byte, 8192)
			n, addr, err := udpConn.ReadFromUDP(payload)
			if err != nil {
				fmt.Println("run_udp_loop ReadFromUDP error", err)
				select {
				case <-cc.cancelCtx.Done():
				case err_chan <- err:
				}
				return
			}
			fmt.Println("run_udp_loop ReadFromUDP", n, "packet.Addr:", addr)
			address, err := common.NewAddressFromAddr(network, addr.String())
			if err != nil {
				fmt.Println("run_udp_loop NewAddressFromAddr error", addr, err)
				select {
				case <-cc.cancelCtx.Done():
				case err_chan <- err:
				}
				return
			}
			select {
			case <-cc.cancelCtx.Done():
			case inboundChan <- &common.UdpPacket{Payload: payload[:n], Addr: address}:
			}
		}
	}()

label_for_outer:
	for {
		select {
		case <-cc.cancelCtx.Done():
			return

		case <-err_chan:
			return

		case packet := <-outboundChan:
			fmt.Println("forward_udp_in_and_out <-outboundChan")
			addr, err := net.ResolveUDPAddr(packet.Addr.Network(), packet.Addr.String())
			if err != nil {
				fmt.Println("forward_udp_in_and_out ResolveUDPAddr error", err, packet)
				continue label_for_outer
			}
			n, err := udpConn.WriteToUDP(packet.Payload, addr)
			if err != nil {
				fmt.Println("forward_udp_in_and_out WriteToUDP error", err)
			} else {
				fmt.Println("forward_udp_in_and_out WriteToUDP success", n)
			}
		}

	}
}

func read_packet_from_tcpconn(cc *ControlChannel, tcpConn *common.MyTcpConn, outboundChan chan *common.UdpPacket, err_chan chan error) {
	for {
		payload := make([]byte, 8192)
		addr, n, err := tcpConn.ReadPacket(payload)
		if err != nil {
			fmt.Println("run_udp_loop ReadPacket error", err)
			select {
			case <-cc.cancelCtx.Done():
			case err_chan <- err:
			}
			return
		}
		fmt.Println("read_packet_from_tcpconn clientTCPConn.ReadPacket", n, "packet.Addr:", addr)
		select {
		case <-cc.cancelCtx.Done():
			return
		case outboundChan <- &common.UdpPacket{Payload: payload[:n], Addr: addr}:
		}
	}
}

func write_packet_to_tcpconn(cc *ControlChannel, tcpConn *common.MyTcpConn, incomingPacketChan chan *common.UdpPacket, udpPacket *common.UdpPacket, err_chan chan error) {
	if udpPacket != nil {
		n, err := tcpConn.WritePacket(udpPacket.Payload, udpPacket.Addr)
		if err != nil {
			fmt.Println("write_packet_to_tcpconn lonely udpPacket tcpConn.WritePacket error", err)
			select {
			case <-cc.cancelCtx.Done():
			case err_chan <- err:
			}
			return
		}
		fmt.Println("write_packet_to_tcpconn lonely updPacket tcpConn.WritePacket success", n)
	}
	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case packet := <-incomingPacketChan:
			n, err := tcpConn.WritePacket(packet.Payload, packet.Addr)
			if err != nil {
				fmt.Println("write_packet_to_tcpconn tcpConn.WritePacket error", err)
				select {
				case <-cc.cancelCtx.Done():
					return
				case err_chan <- err:
				}
				return
			}
			fmt.Println("write_packet_to_tcpconn tcpConn.WritePacket success", n)
		}
	}
}
