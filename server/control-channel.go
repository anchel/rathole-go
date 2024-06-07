package server

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/anchel/rathole-go/common"
	"github.com/anchel/rathole-go/config"
)

type ControlChannel struct {
	session_key  string
	clientConfig *config.ServerConfig
	service      *Service
	data_chan    chan net.Conn
}

func NewControlChannel(session_key string, conf *config.ServerConfig, service *Service) *ControlChannel {
	cc := &ControlChannel{
		session_key:  session_key,
		clientConfig: conf,
		service:      service,
		data_chan:    make(chan net.Conn, 6),
	}

	return cc
}

func (cc *ControlChannel) Close() {
	// todo
}

func (cc *ControlChannel) Run(conn net.Conn) {
	data_req_chan := make(chan bool, 6)

	if cc.service.svcType == config.TCP {
		go run_tcp_loop(cc, data_req_chan)
	} else {
		go run_udp_loop(cc)
	}

	var err error
label_for:
	for {
		var errChan <-chan time.Time
		timerHeartbeat := time.After(55 * time.Second)
		if err != nil {
			errChan = time.After(0)
		}
		select {
		case <-errChan:
			fmt.Println("error occur", err)
			break label_for
		case <-data_req_chan:
			go func() {
				resp := &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Header:     make(map[string][]string),
				}
				resp.Header.Set("cmd", "datachannel")
				err = common.ResponseWriteWithBuffered(resp, conn)
				if err != nil {
					fmt.Println("send /control/cmd datachannel fail", err)
				}
			}()
		case <-timerHeartbeat:
			go func() {
				resp := &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Header:     make(map[string][]string),
				}
				resp.Header.Set("cmd", "heartbeat")
				err = common.ResponseWriteWithBuffered(resp, conn)
				if err != nil {
					fmt.Println("send /control/cmd heartbeat fail", err)
				}
			}()
		}
	}
}

func run_tcp_loop(cc *ControlChannel, data_req_chan chan<- bool) {
	l, err := net.Listen("tcp", cc.service.bind_addr)
	if err != nil {
		fmt.Println("service listen fail", err)
		return
	}
	for {
		remoteConn, err := l.Accept()
		if err != nil {
			fmt.Println("service accept fail", err)
			break
		}
		go forward_tcp_connection(cc, remoteConn, data_req_chan)
	}
}

func forward_tcp_connection(cc *ControlChannel, remoteConn net.Conn, data_req_chan chan<- bool) {
	// defer remoteConn.Close()

	fmt.Println("forward_tcp_connection", remoteConn.RemoteAddr())

	// 发送命令，指示客户端主动连接服务器
	go func() {
		data_req_chan <- true
	}()

	clientConn := <-cc.data_chan
	fmt.Println("成功取得客户的连接", clientConn.RemoteAddr())
	// defer clientConn.Close()

	// go func() {
	// 	for {
	// 		buf := make([]byte, 1024)
	// 		n, e := remoteConn.Read(buf)
	// 		fmt.Println("remoteConn Read", n, e)
	// 		if e != nil {
	// 			fmt.Println("remoteConn Read error", e)
	// 			break
	// 		}
	// 		if n > 0 {
	// 			fmt.Println("remoteConn Read", string(buf[:n]))
	// 			wn, e := clientConn.Write(buf)
	// 			fmt.Println("remoteConn Read and Write to client", wn, e)
	// 		}
	// 	}

	// }()

	err := common.CopyTcpConnection(clientConn, remoteConn)
	if err != nil {
		fmt.Println("CopyTcpConnection error", err)
	} else {
		fmt.Println("CopyTcpConnection success", err)
	}
}

func run_udp_loop(cc *ControlChannel) {

}
