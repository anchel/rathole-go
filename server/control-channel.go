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
	cc.err = errors.New("cc close")
	cc.cancel()
}

func (cc *ControlChannel) Run(conn net.Conn) {
	data_req_chan := make(chan bool, 6)

	if cc.service.svcType == config.TCP {
		go run_tcp_loop(cc, data_req_chan)
	} else {
		go run_udp_loop(cc)
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
				fmt.Println("service accept fail", err)
				break
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

	clientTCPConn, ok := clientConn.(*net.TCPConn)
	if !ok {
		fmt.Println("获取的客户的连接不是tcp连接")
		return
	}

	fmt.Println("成功取得客户的连接", clientConn.RemoteAddr())
	defer clientConn.Close()

	err := common.CopyTcpConnection(clientTCPConn, remoteConn)
	if err != nil {
		fmt.Println("CopyTcpConnection error", err)
	} else {
		fmt.Println("CopyTcpConnection success", err)
	}
}

func run_udp_loop(cc *ControlChannel) {

}
