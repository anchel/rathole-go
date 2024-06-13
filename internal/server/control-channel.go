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
