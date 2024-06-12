package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/anchel/rathole-go/config"
	"github.com/anchel/rathole-go/internal/common"
)

type ControlChannel struct {
	session_key  string
	clientConfig *config.ServerConfig
	service      *Service
	s            *Server
	data_chan    chan net.Conn
	cancelCtx    context.Context
	cancel       context.CancelFunc
	err          error
}

func NewControlChannel(parentCtx context.Context, session_key string, conf *config.ServerConfig, service *Service, s *Server) *ControlChannel {
	ctx, cancel := context.WithCancel(parentCtx)
	cc := &ControlChannel{
		session_key:  session_key,
		clientConfig: conf,
		service:      service,
		s:            s,
		data_chan:    make(chan net.Conn, 6),
		cancelCtx:    ctx,
		cancel:       cancel,
	}

	return cc
}

func (cc *ControlChannel) Close() {
	fmt.Println("ControlCancel Close")
	cc.err = errors.New("server cc close")
	cc.cancel()
}

func (cc *ControlChannel) Run(conn net.Conn) {
	data_req_chan := make(chan bool, 6)

	if cc.service.svcType == config.TCP {
		go run_tcp_loop(cc, data_req_chan)
	} else {
		go run_udp_loop(cc, data_req_chan)
	}

label_for:
	for {
		timerHeartbeat := time.After(20 * time.Second)
		if cc.err != nil {
			fmt.Println("cc for loop: cc.err != nil, break")
			break
		}
		select {
		case <-cc.cancelCtx.Done():
			fmt.Println("ControlChannel receive cancel")
			break label_for
		case <-data_req_chan:
			fmt.Println("start send /contro/cmd datachannel")
			if cc.err != nil {
				continue label_for
			}
			go func() {
				resp := &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Header:     make(map[string][]string),
				}
				resp.Header.Set("cmd", "datachannel")
				cc.err = common.ResponseWriteWithBuffered(resp, conn)
				if cc.err != nil {
					fmt.Println("send /control/cmd datachannel fail", cc.err)
					cc.cancel()
				}
			}()
		case <-timerHeartbeat:
			fmt.Println("start send /contro/cmd heartbeat")
			if cc.err != nil {
				continue label_for
			}
			go func() {
				resp := &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Header:     make(map[string][]string),
				}
				resp.Header.Set("cmd", "heartbeat")
				cc.err = common.ResponseWriteWithBuffered(resp, conn)
				if cc.err != nil {
					fmt.Println("send /control/cmd heartbeat fail", cc.err)
					cc.cancel()
				}
			}()
		}
	}

	fmt.Println("ControlChannel Run Over")
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
					fmt.Println("service accept fail, because of cancel", err)
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

	defer func() {
		fmt.Println("run_udp_loop localUDPConn Close")
		err := localUDPConn.Close()
		if err != nil {
			fmt.Println("run_udp_loop localUDPConn Close error", err)
		}
	}()

	udpPacketChan := make(chan *common.UdpPacket, 32)

	go func() {
	label_for_one:
		for {
			payload := make([]byte, 8192)
			n, addr, err := localUDPConn.ReadFromUDP(payload)
			if err != nil {
				fmt.Println("run_udp_loop ReadFromUDP error", err)
				return
			}
			fmt.Println("run_udp_loop ReadFromUDP", n, addr)
			select {
			case <-cc.cancelCtx.Done():
				break label_for_one
			case udpPacketChan <- &common.UdpPacket{Payload: payload[:n], Addr: addr}:
			}
		}
	}()

	var clientTCPConn *common.MyTcpConn

	defer func() {
		if clientTCPConn != nil {
			err := clientTCPConn.Close()
			if err != nil {
				fmt.Println("run_udp_loop clientTCPConn.Close error", err)
			}
		}
	}()

	var tmpClientTCPConnChan chan *common.MyTcpConn

	var udpPacket *common.UdpPacket

label_for_second:
	for {
		var tmpUdpPacketChan chan *common.UdpPacket
		if clientTCPConn != nil || (clientTCPConn == nil && tmpClientTCPConnChan == nil) {
			tmpUdpPacketChan = udpPacketChan
		} else {
			tmpUdpPacketChan = nil
		}

		select {
		case <-cc.cancelCtx.Done():
			break label_for_second
		case ctc := <-tmpClientTCPConnChan:
			fmt.Println("run_udp_loop <-tmpClientTCPConnChan", ctc.LocalAddr(), ctc.RemoteAddr())
			clientTCPConn = ctc
			tmpClientTCPConnChan = nil
			go forward_packet_tcp_to_udp(cc, clientTCPConn, localUDPConn)
			if udpPacket != nil {
				go forward_packet_udp_to_tcp(cc, clientTCPConn, udpPacket)
			}
		case udpPacket = <-tmpUdpPacketChan:
			fmt.Println("run_udp_loop <-tmpUdpPacketChan")
			if clientTCPConn == nil {
				fmt.Println("run_udp_loop clientTCPConn == nil")
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
			} else {
				fmt.Println("run_udp_loop clientTCPConn != nil")
				go forward_packet_udp_to_tcp(cc, clientTCPConn, udpPacket)
			}
		}
	}

	fmt.Println("run_udp_loop starting to finish")
}

func forward_packet_tcp_to_udp(cc *ControlChannel, clientTCPConn *common.MyTcpConn, localUDPConn *net.UDPConn) {
	udpPacketChan := make(chan *common.UdpPacket, 32)
	go func() {
		for {
			payload := make([]byte, 8192)
			addr, n, err := clientTCPConn.ReadPacket(payload)
			if err != nil {
				fmt.Println("run_udp_loop ReadPacket error", err)
				return
			}
			fmt.Println("forward_packet_tcp_to_udp clientTCPConn.ReadPacket addr", n, addr)
			select {
			case <-cc.cancelCtx.Done():
				return
			case udpPacketChan <- &common.UdpPacket{Payload: payload[:n], Addr: addr}:
			}
		}
	}()

label_for_one:
	for {
		select {
		case <-cc.cancelCtx.Done():
			break label_for_one
		case packet := <-udpPacketChan:
			fmt.Println("forward_packet_tcp_to_udp <-udpPacketChan", packet.Addr.Network(), packet.Addr.String())
			go func() {
				addr, err := net.ResolveUDPAddr(packet.Addr.Network(), packet.Addr.String())
				if err != nil {
					fmt.Println("forward_packet_tcp_to_udp ResolveUDPAddr error", err, packet)
					return
				}
				n, err := localUDPConn.WriteToUDP(packet.Payload, addr)
				if err != nil {
					fmt.Println("forward_packet_tcp_to_udp localUDPConn.WriteToUDP error", err)
				}
				fmt.Println("forward_packet_tcp_to_udp localUDPConn.WriteToUDP success", n)
			}()
		}
	}
}

func forward_packet_udp_to_tcp(cc *ControlChannel, clientTCPConn *common.MyTcpConn, udpPacket *common.UdpPacket) error {
	network := string(cc.service.svcType)

	addr, err := common.NewAddressFromAddr(network, udpPacket.Addr.String())
	if err != nil {
		fmt.Println("forward_packet_udp_to_tcp NewAddressFromAddr error", err)
		return err
	}
	fmt.Println("forward_packet_udp_to_tcp addr", addr.AddressType, addr.NetworkType, addr)
	n, err := clientTCPConn.WritePacket(udpPacket.Payload, addr)
	if err != nil {
		fmt.Println("forward_packet_udp_to_tcp clientTCPConn.WritePacket error", err)
		return err
	}
	fmt.Println("forward_packet_udp_to_tcp clientTCPConn.WritePacket success", n, addr)
	return nil
}
