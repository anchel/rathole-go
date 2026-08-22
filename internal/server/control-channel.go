package server

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
	"golang.org/x/time/rate"
)

var GlobalControlChannelId atomic.Uint64

// 等待控制通道彻底停止的上限，超过就不再干等，避免热更新/重连被单个卡住的通道拖死。
const closeWaitTimeout = 5 * time.Second

type ControlChannel struct {
	id          uint64
	session_key string

	service *Service

	connection       *net.TCPConn
	reader           *bufio.Reader
	data_chan        chan net.Conn
	num_data_channel atomic.Int32

	// 数据通道表会被速率监控和主循环两个 goroutine 同时访问，必须加锁。
	// 裸 map 的并发读写会触发 fatal error: concurrent map read and map write，
	// 这种错误 recover 拦不住，整个进程直接退出。
	muDatachannel      sync.Mutex
	datachannelConnMap map[uint64]*DatachannelConn
	nextDatachannelId  uint64

	inboundPacketChan  chan *common.UdpPacket // for udp
	outboundPacketChan chan *common.UdpPacket // for udp

	cancelCtx context.Context
	cancel    context.CancelFunc

	muState  sync.Mutex
	canceled bool
	started  bool

	done     chan struct{}
	doneOnce sync.Once
}

func NewControlChannel(parentCtx context.Context, session_key string, service *Service, conn *net.TCPConn, reader *bufio.Reader) *ControlChannel {
	ctx, cancel := context.WithCancel(parentCtx)
	cc := &ControlChannel{
		id:          GlobalControlChannelId.Add(1),
		session_key: session_key,

		service: service,

		connection: conn,
		reader:     reader,
		data_chan:  make(chan net.Conn, 6),

		datachannelConnMap: make(map[uint64]*DatachannelConn),

		inboundPacketChan:  make(chan *common.UdpPacket, 32),
		outboundPacketChan: make(chan *common.UdpPacket, 32),

		cancelCtx: ctx,
		cancel:    cancel,

		done: make(chan struct{}),
	}

	return cc
}

// Close 停止控制通道，并等到 Run 真正退出（监听端口已释放）才返回。
// RunnableManager.Put 依赖这个同步语义：同一个服务的新通道要复用同一个 bind_addr，
// 旧通道的监听端口没释放的话新通道会直接 listen 失败。
func (cc *ControlChannel) Close() {
	fmt.Println("ControlCancel Close")
	cc.Cancel()
	select {
	case <-cc.done:
	case <-time.After(closeWaitTimeout):
		fmt.Println("cc Close 等待退出超时", cc.id)
	}
}

func (cc *ControlChannel) Cancel() {
	cc.muState.Lock()
	neverRun := false
	if !cc.canceled {
		cc.canceled = true
		cc.cancel()
		// Run 还没开始就被取消，它不会再跑了，得自己把 done 关掉，
		// 否则 Close 只能干等到超时。
		neverRun = !cc.started
	}
	cc.muState.Unlock()

	if neverRun {
		cc.markDone()
	}
}

func (cc *ControlChannel) markDone() {
	cc.doneOnce.Do(func() {
		close(cc.done)
	})
}

func (cc *ControlChannel) Run() {
	// 注册得最早 => 最后执行，保证 done 关闭时资源已经全部释放完
	defer cc.markDone()

	defer func() {
		fmt.Println("cc Run end")
	}()

	defer func() {
		err := cc.connection.Close()
		if err != nil {
			fmt.Println("cc cc.connection.Close error", err)
		}
	}()

	cc.muState.Lock()
	if cc.canceled {
		cc.muState.Unlock()
		fmt.Println("cc Run stop, because of canceled")
		return
	}
	cc.started = true
	cc.muState.Unlock()

	cmd_chan := make(chan string, 1)
	err_chan := make(chan error, 1)
	datachannel_req_chan := make(chan bool, 6)

	go cc.do_read_cmd(cmd_chan, err_chan)

	// 下面两个都往 cc.connection 写。单次 ResponseWriteWithBuffered 的内容远小于
	// bufio 的 4096 缓冲，只会产生一次 Write 调用，而 net.TCPConn.Write 内部是加锁的，
	// 所以帧不会交错。响应体一旦变大到需要多次 flush，这里就必须自己加锁。
	go cc.do_send_heartbeat_to_client(err_chan)
	go cc.do_send_create_datachannel(datachannel_req_chan, err_chan)

	// 转发循环要等它退出后 Run 才能返回：它负责关闭对外的监听端口。
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if cc.service.svcType == config.TCP {
			cc.run_tcp_loop(datachannel_req_chan, err_chan)
		} else {
			cc.run_udp_loop(datachannel_req_chan, err_chan)
		}
	}()

	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()

label_for:
	for {
		select {
		case <-cc.cancelCtx.Done():
			fmt.Println("cc receive cancelCtx.Done()")
			break label_for

		case err := <-err_chan:
			fmt.Println("cc <-err_chan error", err)
			break label_for

		case cmd := <-cmd_chan:
			if cmd == "heartbeat" {
				fmt.Println("cc recv client heartbeat")
			} else {
				fmt.Println("cc recv client other cmd", cmd)
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(120 * time.Second)

		case <-timer.C:
			fmt.Println("cc recv client heartbeat timeout")
			break label_for
		}
	}

	cc.Cancel()
	wg.Wait()

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

	ticker := time.NewTicker(110 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-ticker.C:
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
	if cc.service.svcType == config.UDP {
		return int32(cc.datachannelCount())
	}
	return cc.num_data_channel.Load()
}

func (cc *ControlChannel) ClientAddr() string {
	return cc.connection.RemoteAddr().String()
}

func (cc *ControlChannel) datachannelCount() int {
	cc.muDatachannel.Lock()
	defer cc.muDatachannel.Unlock()
	return len(cc.datachannelConnMap)
}

func (cc *ControlChannel) addDatachannel(conn *common.MyTcpConn) *DatachannelConn {
	cc.muDatachannel.Lock()
	defer cc.muDatachannel.Unlock()
	cc.nextDatachannelId++
	// 用自增 id 而不是 RemoteAddr 做 key：连接关闭后端口可能被复用，
	// 用地址做 key 时旧通道的收尾清理会误删掉新通道的条目。
	dcConn := NewDatachannelConn(cc.nextDatachannelId, conn)
	cc.datachannelConnMap[dcConn.id] = dcConn
	return dcConn
}

func (cc *ControlChannel) removeDatachannel(id uint64) {
	cc.muDatachannel.Lock()
	defer cc.muDatachannel.Unlock()
	delete(cc.datachannelConnMap, id)
}

// finishOneDatachannel 任选一条数据通道结束掉，返回是否真的关掉了一条。
func (cc *ControlChannel) finishOneDatachannel() bool {
	cc.muDatachannel.Lock()
	var target *DatachannelConn
	for _, dcConn := range cc.datachannelConnMap {
		target = dcConn
		break
	}
	if target != nil {
		delete(cc.datachannelConnMap, target.id)
	}
	cc.muDatachannel.Unlock()

	if target == nil {
		return false
	}
	target.Finish()
	return true
}

func (cc *ControlChannel) finishAllDatachannels() {
	cc.muDatachannel.Lock()
	list := make([]*DatachannelConn, 0, len(cc.datachannelConnMap))
	for id, dcConn := range cc.datachannelConnMap {
		list = append(list, dcConn)
		delete(cc.datachannelConnMap, id)
	}
	cc.muDatachannel.Unlock()

	for _, dcConn := range list {
		dcConn.Finish()
	}
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
	tcpAddr, err := net.ResolveTCPAddr("tcp", cc.service.bind_addr)
	if err != nil {
		fmt.Println("cc run_tcp_loop bind_addr invalid", cc.service.bind_addr, err)
		common.SendError(cc.cancelCtx, err_chan, err)
		return
	}
	l, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		fmt.Println("cc run_tcp_loop service listen fail", err)
		common.SendError(cc.cancelCtx, err_chan, err)
		return
	}
	fmt.Println("cc run_tcp_loop service listen success", l.Addr())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			remoteConn, err := l.AcceptTCP()
			if err != nil {
				select {
				case <-cc.cancelCtx.Done():
					fmt.Println("cc run_tcp_loop service accept fail, because of cancelCtx.Done()", err)
				default:
					fmt.Println("cc run_tcp_loop service accept fail", err)
					common.SendError(cc.cancelCtx, err_chan, err)
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
	// 等 Accept 循环真正退出，端口这时才算彻底释放
	wg.Wait()
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
	cc.num_data_channel.Add(1)

	defer func() {
		err := clientTCPConn.Close()
		if err != nil {
			fmt.Println("forward_tcp_connection clientTCPConn.Close error", err)
		}
		cc.num_data_channel.Add(-1)
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

	defer cc.finishAllDatachannels()

	const clientConnMax = 5
	counter_chan := make(chan bool, 64)
	add_trigger_chan := make(chan bool, clientConnMax)
	reduce_trigger_chan := make(chan bool, clientConnMax)

	go cc.forward_udp_in_and_out(udpConnListening, cc.inboundPacketChan, cc.outboundPacketChan, counter_chan, err_chan)
	go cc.monitor_udp_speed(counter_chan, add_trigger_chan, reduce_trigger_chan, clientConnMax)

	for {
		select {
		case <-cc.cancelCtx.Done():
			return

		case <-add_trigger_chan:
			if cc.datachannelCount() >= clientConnMax {
				fmt.Println("cc run_udp_loop datachannel 数量已达上限")
				continue
			}
			fmt.Println("cc run_udp_loop create datachannel")
			clientTCPConn := cc.acquire_data_channel(datachannel_req_chan)
			if clientTCPConn == nil {
				// 单次取不到数据通道只是本次失败，不能把整条控制通道拆掉：
				// 那会让所有 udp 转发一起中断并逼客户端重连。速率监控下个周期会再试。
				fmt.Println("cc run_udp_loop acquire_data_channel fail, 稍后重试")
				continue
			}
			fmt.Println("cc run_udp_loop create datachannel success", clientTCPConn.RemoteAddr())
			go cc.do_with_datachannel(cc.addDatachannel(clientTCPConn))

		case <-reduce_trigger_chan:
			if !cc.finishOneDatachannel() {
				fmt.Println("cc run_udp_loop 没有可回收的 datachannel")
			}
		}
	}
}

func (cc *ControlChannel) monitor_udp_speed(counter_chan chan bool, add_trigger_chan chan bool, reduce_trigger_chan chan bool, clientConnMax int) {
	defer func() {
		fmt.Println("cc run_udp_loop speed monitor end")
	}()

	limiter := rate.NewLimiter(rate.Every(1*time.Second), 1)
	counter := 0
	start := time.Now()

	var speed_monitor float64 = 5 // 速率监控阈值，超过这个值就增加datachannel

	// 用 Ticker 而不是 Timer+Reset：对已经触发但没排空 C 的 Timer 调用 Reset，
	// 残留的旧值会让下一次接收立刻返回，导致回收逻辑被误触发。
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	trySend := func(ch chan bool) {
		select {
		case ch <- true:
		default: // 触发信号已经堆积，丢掉即可，绝不能让监控 goroutine 阻塞
		}
	}

	// allowAdd 为 false 表示这轮是空闲期的例行检查，只回收不新建，
	// 否则没有流量时会陷入「建了又回收」的抖动。
	evaluate := func(allowAdd bool) {
		elapsed := time.Since(start)
		r := float64(counter) / elapsed.Seconds()
		counter = 0
		start = time.Now()

		n := cc.datachannelCount()
		common.Debug("cc run_udp_loop speed monitor", r, "packet/s, datachannel:", n)

		if allowAdd && (n < 1 || r > speed_monitor) && n < clientConnMax {
			trySend(add_trigger_chan)
		} else if n > 0 && r < speed_monitor/2 {
			trySend(reduce_trigger_chan)
		}
	}

	for {
		select {
		case <-cc.cancelCtx.Done():
			return
		case <-ticker.C:
			evaluate(false)
		case <-counter_chan:
			counter += 1
			if limiter.Allow() {
				evaluate(true)
			}
		}
	}
}

func (cc *ControlChannel) do_with_datachannel(dcConn *DatachannelConn) {
	defer func() {
		fmt.Println("cc do_with_datachannel end")
		cc.removeDatachannel(dcConn.id)
		dcConn.Finish()
		err := dcConn.ClientTcpConn.Close()
		if err != nil {
			fmt.Println("cc do_with_datachannel ClientTcpConn.Close error", err)
		}
	}()

	go cc.write_packet_to_tcpconn(dcConn, cc.inboundPacketChan)
	go cc.read_packet_from_tcpconn(dcConn, cc.outboundPacketChan)

	select {
	case <-cc.cancelCtx.Done():
	case <-dcConn.FinishChan:
	}
}

func (cc *ControlChannel) acquire_data_channel(datachannel_req_chan chan<- bool) *common.MyTcpConn {
	defer func() {
		fmt.Println("cc acquire_data_channel end")
	}()

	// 这里不再另起 goroutine 去等连接。原来的写法在外层超时返回后，
	// 内层 goroutine 仍挂在 cc.data_chan 上，会把后来到达的、本该给其他请求的
	// 数据通道抢走并永久持有：连接不关也不用，goroutine 和 fd 一起泄漏，
	// 而且会引发「A 超时抢走 B 的连接导致 B 也超时」的级联。
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()

	select {
	case <-cc.cancelCtx.Done():
		return nil
	case datachannel_req_chan <- true:
	case <-timer.C:
		fmt.Println("cc acquire_data_channel 投递建立请求超时")
		return nil
	}

	for {
		select {
		case <-cc.cancelCtx.Done():
			return nil
		case <-timer.C:
			fmt.Println("cc acquire_data_channel 等待客户端建立数据通道超时")
			return nil
		case conn := <-cc.data_chan:
			clientTCPConn, ok := conn.(*common.MyTcpConn)
			if !ok {
				fmt.Println("cc acquire_data_channel 获取的客户端连接不是tcp连接")
				continue
			}
			return clientTCPConn
		}
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
			payload := make([]byte, common.MaxPacketSize)
			n, addr, err := udpConnListening.ReadFromUDP(payload)
			if err != nil {
				fmt.Println("datachannel run_udp_loop ReadFromUDP error", err)
				common.SendError(cc.cancelCtx, local_err_chan, err)
				return
			}
			common.Debug("datachannel run_udp_loop ReadFromUDP", n, "packet.Addr:", addr)
			address, err := common.NewAddressFromAddr(network, addr.String())
			if err != nil {
				fmt.Println("datachannel run_udp_loop NewAddressFromAddr error", addr, err)
				continue
			}

			// 这两个投递都必须是非阻塞的。监听 socket 只有一条，一旦在这里阻塞住，
			// 速率统计也跟着停摆，监控就再也不会去创建新的数据通道，形成永久卡死。
			// udp 本身允许丢包，队列满时丢弃是正确取舍。
			select {
			case counter_chan <- true: // 计算速率用途
			default:
			}

			select {
			case inboundChan <- &common.UdpPacket{Payload: payload[:n], Addr: address}:
			default:
				common.Debug("datachannel run_udp_loop inbound 队列已满，丢弃数据包", addr)
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
			// 单个数据包的问题不该拖垮整条控制通道，记录后继续处理下一个即可。
			addr, err := net.ResolveUDPAddr(packet.Addr.Network(), packet.Addr.String())
			if err != nil {
				fmt.Println("datachannel forward_udp_in_and_out ResolveUDPAddr error", err, packet.Addr)
				continue
			}
			n, err := udpConnListening.WriteToUDP(packet.Payload, addr)
			if err != nil {
				fmt.Println("datachannel forward_udp_in_and_out WriteToUDP error", addr, err)
				continue
			}
			common.Debug("datachannel forward_udp_in_and_out WriteToUDP success", n, addr)
		}
	}
}

func (cc *ControlChannel) read_packet_from_tcpconn(dcConn *DatachannelConn, outboundChan chan *common.UdpPacket) {
	defer func() {
		fmt.Println("datachannel read_packet_from_tcpconn end")
		// 读出错说明这条数据通道已经不可用了，必须把它拆掉。
		// 原来这里往 nil 的 err_chan 发错误，会永久阻塞：通道死了却还挂在表里，
		// 继续分到数据包然后全部丢弃。
		dcConn.Finish()
	}()

	ctx := cc.cancelCtx

	for {
		payload := make([]byte, common.MaxPacketSize)
		addr, n, err := dcConn.ClientTcpConn.ReadPacket(payload)
		if err != nil {
			fmt.Println("datachannel read_packet_from_tcpconn ReadPacket error", err)
			return
		}
		common.Debug("datachannel read_packet_from_tcpconn tcpConn.ReadPacket", n, "packet.Addr:", addr)

		select {
		case <-ctx.Done():
			return
		case <-dcConn.FinishChan:
			return
		case outboundChan <- &common.UdpPacket{Payload: payload[:n], Addr: addr}:
		}
	}
}

func (cc *ControlChannel) write_packet_to_tcpconn(dcConn *DatachannelConn, incomingPacketChan chan *common.UdpPacket) {
	defer func() {
		fmt.Println("datachannel write_packet_to_tcpconn end")
		dcConn.Finish()
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
				return
			}
			common.Debug("datachannel write_packet_to_tcpconn tcpConn.WritePacket success", n)
		}
	}
}
