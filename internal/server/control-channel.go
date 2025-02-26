package server

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
	"golang.org/x/time/rate"
)

var GlobalControlChannelId int = 0

type ControlChannel struct {
	id          int
	session_key string

	service *Service

	connection       *net.TCPConn
	reader           *bufio.Reader
	data_chan        chan net.Conn
	num_data_channel int32

	datachannelConnMap map[string]*DatachannelConn

	inboundPacketChan  chan *common.UdpPacket // for udp
	outboundPacketChan chan *common.UdpPacket // for udp

	cancelCtx context.Context
	cancel    context.CancelFunc

	muCancel sync.Mutex
	canceled bool
}

func NewControlChannel(parentCtx context.Context, session_key string, service *Service, conn *net.TCPConn, reader *bufio.Reader) *ControlChannel {
	ctx, cancel := context.WithCancel(parentCtx)
	cc := &ControlChannel{
		id:          GlobalControlChannelId,
		session_key: session_key,

		service: service,

		connection:       conn,
		reader:           reader,
		data_chan:        make(chan net.Conn, 6),
		num_data_channel: 0,

		datachannelConnMap: make(map[string]*DatachannelConn),

		inboundPacketChan:  make(chan *common.UdpPacket, 32),
		outboundPacketChan: make(chan *common.UdpPacket, 32),

		cancelCtx: ctx,
		cancel:    cancel,
	}

	return cc
}

func (cc *ControlChannel) Close() {
	fmt.Println("ControlCancel Close")
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

func (cc *ControlChannel) Run() {
	defer func() {
		fmt.Println("cc Run end")
	}()

	defer func() {
		err := cc.connection.Close()
		if err != nil {
			fmt.Println("cc cc.connection.Close error", err)
		}
	}()

	if cc.canceled {
		fmt.Println("cc Run stop, because of canceled")
		return
	}

	defer cc.Cancel()

	cmd_chan := make(chan string, 1)
	err_chan := make(chan error, 1)
	datachannel_req_chan := make(chan bool, 6)

	go cc.do_read_cmd(cmd_chan, err_chan)

	// 下面两个，涉及对net.Conn的并发调用，可能会有数据竞争问题
	go cc.do_send_heartbeat_to_client(err_chan)
	go cc.do_send_create_datachannel(datachannel_req_chan, err_chan)

	if cc.service.svcType == config.TCP {
		go cc.run_tcp_loop(datachannel_req_chan, err_chan)
	} else {
		go cc.run_udp_loop(datachannel_req_chan, err_chan)
	}

label_for:
	for {
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

func (cc *ControlChannel) do_read_cmd(cmd_chan chan string, err_chan chan error) {
	defer func() {
		fmt.Println("cc do_read_cmd end")
	}()

	for {
		resp, err := http.ReadResponse(cc.reader, nil)
		if err != nil {
			select {
			case <-cc.cancelCtx.Done():
				fmt.Println("cc recv client /control/cmd fail, cancelCtx.Done()")
			default:
				fmt.Println("cc recv client /control/cmd fail", err)
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

func (cc *ControlChannel) do_send_create_datachannel(datachannel_req_chan chan bool, err_chan chan error) {
	defer func() {
		fmt.Println("cc do_send_create_datachannel end")
	}()

	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-datachannel_req_chan:
			fmt.Println("cc start send to client /contro/cmd datachannel")
			resp := &http.Response{
				Proto:      "HTTP/1.1",
				ProtoMajor: 1,
				ProtoMinor: 1,
				Status:     "200 OK",
				StatusCode: http.StatusOK,
				Header:     make(map[string][]string),
			}
			resp.Header.Set("cmd", "datachannel")
			err := common.ResponseWriteWithBuffered(resp, cc.connection)
			if err != nil {
				fmt.Println("cc send to client /control/cmd datachannel fail", err)
				common.SendError(cc.cancelCtx, err_chan, err)
				return
			}
		}
	}
}

func (cc *ControlChannel) do_send_heartbeat_to_client(err_chan chan error) {
	defer func() {
		fmt.Println("cc do_send_heartbeat_to_client end")
	}()

	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-time.After(110 * time.Second):
			fmt.Println("cc start send to client /contro/cmd heartbeat")
			resp := &http.Response{
				Proto:      "HTTP/1.1",
				ProtoMajor: 1,
				ProtoMinor: 1,
				Status:     "200 OK",
				StatusCode: http.StatusOK,
				Header:     make(map[string][]string),
			}
			resp.Header.Set("cmd", "heartbeat")
			err := common.ResponseWriteWithBuffered(resp, cc.connection)
			if err != nil {
				fmt.Println("cc send to client /control/cmd heartbeat fail", err)
				common.SendError(cc.cancelCtx, err_chan, err)
				return
			}
		}
	}
}

func (cc *ControlChannel) NumDataChannel() int32 {
	return cc.num_data_channel
}

func (cc *ControlChannel) ClientAddr() string {
	return cc.connection.RemoteAddr().String()
}

func (cc *ControlChannel) run_tcp_loop(datachannel_req_chan chan<- bool, err_chan chan error) {
	fmt.Println("cc run_tcp_loop start")

	defer func() {
		fmt.Println("cc run_tcp_loop end")
	}()

	if cc.canceled {
		fmt.Println("cc run_tcp_loop stop, because of canceled")
		return
	}
	tcpAddr, _ := net.ResolveTCPAddr("tcp", cc.service.bind_addr)
	l, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		fmt.Println("cc run_tcp_loop service listen fail", err)
		select {
		case <-cc.cancelCtx.Done():
		case err_chan <- err:
		}
		return
	}
	fmt.Println("cc run_tcp_loop service listen success", l.Addr())

	go func() {
		for {
			remoteConn, err := l.AcceptTCP()
			if err != nil {
				select {
				case <-cc.cancelCtx.Done():
					fmt.Println("cc run_tcp_loop service accept fail, because of cancelCtx.Done()", err)
				default:
					fmt.Println("cc run_tcp_loop service accept fail", err)
					select {
					case <-cc.cancelCtx.Done():
					case err_chan <- err:
					}
				}
				return
			}
			go cc.forward_tcp_connection(remoteConn, datachannel_req_chan)
		}
	}()

	<-cc.cancelCtx.Done()
	fmt.Println("cc run_tcp_loop start to l.Close")
	err = l.Close()
	if err != nil {
		fmt.Println("cc run_tcp_loop l.Close error", err)
	}
}

func (cc *ControlChannel) forward_tcp_connection(remoteConn *net.TCPConn, datachannel_req_chan chan<- bool) {
	defer func() {
		err := remoteConn.Close()
		if err != nil {
			fmt.Println("forward_tcp_connection remoteConn.Close error", err)
		}
	}()

	fmt.Println("forward_tcp_connection", remoteConn.RemoteAddr())

	clientTCPConn := cc.acquire_data_channel(datachannel_req_chan)
	if clientTCPConn == nil {
		fmt.Println("forward_tcp_connection 获取客户端连接失败")
		return
	}

	fmt.Println("成功取得客户的连接", clientTCPConn.RemoteAddr())
	cc.num_data_channel += 1

	defer func() {
		err := clientTCPConn.Close()
		if err != nil {
			fmt.Println("forward_tcp_connection clientTCPConn.Close error", err)
		}
		cc.num_data_channel -= 1
	}()

	err := common.CopyTcpConnection(cc.cancelCtx, clientTCPConn, remoteConn)
	if err != nil {
		fmt.Println("CopyTcpConnection error", err)
	} else {
		fmt.Println("CopyTcpConnection success", err)
	}
}

func (cc *ControlChannel) run_udp_loop(datachannel_req_chan chan<- bool, err_chan chan error) {
	fmt.Println("cc run_udp_loop start")

	defer func() {
		fmt.Println("cc run_udp_loop end")
	}()

	if cc.canceled {
		fmt.Println("cc run_udp_loop stop, because of canceled")
		return
	}

	network := string(cc.service.svcType)

	laddr, err := net.ResolveUDPAddr(network, cc.service.bind_addr)
	if err != nil {
		fmt.Println("cc run_udp_loop laddr error", err)
		common.SendError(cc.cancelCtx, err_chan, err)
		return
	}

	udpConnListening, err := net.ListenUDP(network, laddr)
	if err != nil {
		fmt.Println("cc run_udp_loop ListenUDP error", err)
		common.SendError(cc.cancelCtx, err_chan, err)
		return
	}

	fmt.Println("cc run_udp_loop udpConnListening.LocalAddr", udpConnListening.LocalAddr())

	defer func() {
		fmt.Println("cc run_udp_loop udpConnListening Close")
		err := udpConnListening.Close()
		if err != nil {
			fmt.Println("cc run_udp_loop udpConnListening Close error", err)
		}
	}()

	clientConnMax := 5
	counter_chan := make(chan bool, 64)
	add_trigger_chan := make(chan bool, clientConnMax)
	reduce_trigger_chan := make(chan bool, clientConnMax)

	go cc.forward_udp_in_and_out(udpConnListening, cc.inboundPacketChan, cc.outboundPacketChan, counter_chan, err_chan)

	go func() {
		defer func() {
			fmt.Println("cc run_udp_loop speed monitor end")
		}()

		limiter := rate.NewLimiter(rate.Every(1*time.Second), 1)
		counter := 0
		start := time.Now()

		var speed_monitor float64 = 5 // 速率监控阈值，超过这个值就增加datachannel

		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()

		for {
			select {
			case <-cc.cancelCtx.Done():
				return
			case tm := <-timer.C:
				fmt.Println("cc run_udp_loop speed monitor timer", tm)
				elapsed := time.Since(start)
				r := float64(counter) / elapsed.Seconds()
				fmt.Println("cc run_udp_loop speed monitor counter:", counter, ", ", r, "packet/s")
				counter = 0
				start = time.Now()
				if len(cc.datachannelConnMap) > 0 && r < speed_monitor/2 {
					fmt.Println("cc run_udp_loop speed monitor, clientConnMap is not empty and speed r < ", speed_monitor/2, ", reduce datachannel")
					select {
					case <-cc.cancelCtx.Done():
						return
					case reduce_trigger_chan <- true:
					}
				}
				timer.Reset(3 * time.Second)

			case <-counter_chan:
				counter += 1
				if limiter.Allow() {
					fmt.Println("cc run_udp_loop speed monitor counter_chan and limiter.Allow")
					elapsed := time.Since(start)
					r := float64(counter) / elapsed.Seconds()
					fmt.Println("cc run_udp_loop speed monitor counter:", counter, ", ", r, "packet/s")
					counter = 0
					start = time.Now()
					if len(cc.datachannelConnMap) < 1 || r > speed_monitor {
						fmt.Println("cc run_udp_loop speed monitor, clientConnMap is empty or speed r > ", speed_monitor, ", create datachannel")
						select {
						case <-cc.cancelCtx.Done():
							return
						case add_trigger_chan <- true:
						}
					}
					if len(cc.datachannelConnMap) > 0 && r < speed_monitor/2 {
						fmt.Println("cc run_udp_loop speed monitor, clientConnMap is not empty and speed r < ", speed_monitor/2, ", reduce datachannel")
						select {
						case <-cc.cancelCtx.Done():
							return
						case reduce_trigger_chan <- true:
						}
					}
				}
				timer.Reset(3 * time.Second)
			}
		}
	}()

	for {
		select {
		case <-cc.cancelCtx.Done():
			return

		case <-add_trigger_chan:
			if len(cc.datachannelConnMap) >= clientConnMax {
				fmt.Println("cc run_udp_loop clientConnMap >= clientConnMax")
				continue
			}
			fmt.Println("cc run_udp_loop create datachannel")
			clientTCPConn := cc.acquire_data_channel(datachannel_req_chan)
			if clientTCPConn == nil {
				fmt.Println("cc run_udp_loop acquire_data_channel fail")
				common.SendError(cc.cancelCtx, err_chan, err)
				return
			}
			fmt.Println("cc run_udp_loop create datachannel success", clientTCPConn.RemoteAddr())
			datachannelConn := &DatachannelConn{ClientTcpConn: clientTCPConn, FinishChan: make(chan bool, 1)}
			cc.datachannelConnMap[clientTCPConn.RemoteAddr().String()] = datachannelConn
			cc.num_data_channel += 1
			go cc.do_with_datachannel(datachannelConn)

		case <-reduce_trigger_chan:
			if len(cc.datachannelConnMap) <= 0 {
				fmt.Println("cc run_udp_loop clientConnMap <= 0")
				continue
			}
			fmt.Println("cc run_udp_loop reduce datachannel")
			for addr, dcConn := range cc.datachannelConnMap {
				fmt.Println("cc run_udp_loop reduce datachannel", addr)
				select {
				case <-cc.cancelCtx.Done():
					return
				case dcConn.FinishChan <- true:
					close(dcConn.FinishChan) // 这里需要关闭，因为有多处地方会读取这个chan，而容量为1不足以支持多处读取
				}
				delete(cc.datachannelConnMap, addr)
				cc.num_data_channel -= 1
				break
			}
		}
	}
}

func (cc *ControlChannel) do_with_datachannel(dcConn *DatachannelConn) {
	defer func() {
		fmt.Println("cc do_with_datachannel end")
		err := dcConn.ClientTcpConn.Close()
		if err != nil {
			fmt.Println("cc do_with_datachannel ClientTcpConn.Close error", err)
		}
	}()

	go cc.write_packet_to_tcpconn(dcConn, cc.inboundPacketChan, nil)
	go cc.read_packet_from_tcpconn(dcConn, cc.outboundPacketChan, nil)

	select {
	case <-cc.cancelCtx.Done():
		return
	case <-dcConn.FinishChan:
		return
	}
}

func (cc *ControlChannel) acquire_data_channel(datachannel_req_chan chan<- bool) *common.MyTcpConn {
	defer func() {
		fmt.Println("cc acquire_data_channel end")
	}()

	local_data_chan := make(chan *common.MyTcpConn)

	go func() {
		defer func() {
			fmt.Println("cc acquire_data_channel select end")
		}()

		select {
		case <-cc.cancelCtx.Done():
			return
		case datachannel_req_chan <- true:
		}

		var clientTCPConn *common.MyTcpConn
		var ok bool
		select {
		case <-cc.cancelCtx.Done():
			return
		case conn := <-cc.data_chan:
			clientTCPConn, ok = conn.(*common.MyTcpConn)
			if !ok {
				fmt.Println("cc run_udp_loop 获取的客户的连接不是tcp连接")
				return
			}
		}

		select {
		case <-cc.cancelCtx.Done():
			return
		case local_data_chan <- clientTCPConn:
		}
	}()

	select {
	case <-cc.cancelCtx.Done():
		return nil
	case conn := <-local_data_chan:
		return conn
	case <-time.After(6 * time.Second):
		return nil
	}
}

func (cc *ControlChannel) forward_udp_in_and_out(udpConnListening *net.UDPConn, inboundChan chan *common.UdpPacket, outboundChan chan *common.UdpPacket, counter_chan chan bool, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel forward_udp_in_and_out end")
	}()

	network := string(cc.service.svcType)

	local_err_chan := make(chan error, 3)
	go func() {
		defer func() {
			fmt.Println("datachannel forward_udp_in_and_out udpConnListening.ReadFromUDP end")
		}()

		for {
			payload := make([]byte, 8192)
			n, addr, err := udpConnListening.ReadFromUDP(payload)
			if err != nil {
				fmt.Println("datachannel run_udp_loop ReadFromUDP error", err)
				common.SendError(cc.cancelCtx, local_err_chan, err)
				return
			}
			fmt.Println("datachannel run_udp_loop ReadFromUDP", n, "packet.Addr:", addr)
			address, err := common.NewAddressFromAddr(network, addr.String())
			if err != nil {
				fmt.Println("datachannel run_udp_loop NewAddressFromAddr error", addr, err)
				common.SendError(cc.cancelCtx, local_err_chan, err)
				return
			}

			// 计算速率用途
			select {
			case <-cc.cancelCtx.Done():
				return
			case counter_chan <- true:
			}

			select {
			case <-cc.cancelCtx.Done():
				return
			case inboundChan <- &common.UdpPacket{Payload: payload[:n], Addr: address}:
			}
		}
	}()

	for {
		select {
		case <-cc.cancelCtx.Done():
			return

		case err := <-local_err_chan:
			common.SendError(cc.cancelCtx, err_chan, err)
			return

		case packet := <-outboundChan:
			addr, err := net.ResolveUDPAddr(packet.Addr.Network(), packet.Addr.String())
			if err != nil {
				fmt.Println("datachannel forward_udp_in_and_out ResolveUDPAddr error", err, packet)
				common.SendError(cc.cancelCtx, err_chan, err) // 这里是否有必要发送错误来终止controlchannel
				return
			}
			n, err := udpConnListening.WriteToUDP(packet.Payload, addr)
			if err != nil {
				fmt.Println("datachannel forward_udp_in_and_out WriteToUDP error", addr, err)
				common.SendError(cc.cancelCtx, err_chan, err) // 这里是否有必要发送错误来终止controlchannel
				return
			} else {
				fmt.Println("datachannel forward_udp_in_and_out WriteToUDP success", n, addr)
			}
		}
	}
}

func (cc *ControlChannel) read_packet_from_tcpconn(dcConn *DatachannelConn, outboundChan chan *common.UdpPacket, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel read_packet_from_tcpconn end")
	}()

	ctx := cc.cancelCtx

	localCh := make(chan *common.UdpPacket, 32)
	go func() {
		defer func() {
			fmt.Println("datachannel read_packet_from_tcpconn tcpConn.ReadPacket end")
		}()

		for {
			payload := make([]byte, 8192)
			addr, n, err := dcConn.ClientTcpConn.ReadPacket(payload)
			if err != nil {
				fmt.Println("datachannel read_packet_from_tcpconn ReadPacket error", err)
				common.SendError(ctx, err_chan, err)
				return
			}
			fmt.Println("datachannel read_packet_from_tcpconn tcpConn.ReadPacket", n, "packet.Addr:", addr)
			select {
			case <-ctx.Done():
				return
			case <-dcConn.FinishChan:
				return
			case localCh <- &common.UdpPacket{Payload: payload[:n], Addr: addr}:
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-dcConn.FinishChan:
			return
		case packet := <-localCh:
			select {
			case <-ctx.Done():
				return
			case <-dcConn.FinishChan:
				return
			case outboundChan <- packet:
			}
		}
	}
}

func (cc *ControlChannel) write_packet_to_tcpconn(dcConn *DatachannelConn, incomingPacketChan chan *common.UdpPacket, err_chan chan error) {
	defer func() {
		fmt.Println("datachannel write_packet_to_tcpconn end")
	}()

	ctx := cc.cancelCtx

	for {
		select {
		case <-ctx.Done():
			return
		case <-dcConn.FinishChan:
			return
		case packet := <-incomingPacketChan:
			n, err := dcConn.ClientTcpConn.WritePacket(packet.Payload, packet.Addr)
			if err != nil {
				fmt.Println("datachannel write_packet_to_tcpconn tcpConn.WritePacket error", err)
				common.SendError(ctx, err_chan, err)
				return
			}
			fmt.Println("datachannel write_packet_to_tcpconn tcpConn.WritePacket success", n)
		}
	}
}
