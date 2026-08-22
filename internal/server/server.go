package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
)

type Digest string

type Service struct {
	svcName   string
	svcType   config.ServiceType
	token     string
	bind_addr string
}

// ServiceView 是配置的只读快照，给 http 管理页用。
// 直接读 s.config 会和热更新的写操作构成数据竞争。
type ServiceView struct {
	Name     string
	Type     config.ServiceType
	Token    string
	BindAddr string
}

type Server struct {
	config                *config.ServerConfig
	services              map[Digest]*Service
	controlChannelManager *common.RunnableManager
	mu                    sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc

	muDone   sync.Mutex
	canceled bool
}

func NewServer(parentCtx context.Context, conf *config.ServerConfig) *Server {
	baseCtx := context.WithValue(parentCtx, common.ContextKey("remoteAddr"), conf.BindAddr)
	ctx, cancel := context.WithCancel(baseCtx)

	s := &Server{}
	s.config = conf

	s.controlChannelManager = common.NewRunnableManager()
	s.ctx = ctx
	s.cancel = cancel

	return s
}

func (s *Server) Run(sigChan chan os.Signal, updater chan *config.Config) {
	s.mu.Lock()
	s.init()
	s.mu.Unlock()
	s.acceptLoop(sigChan, updater)
}

// init 重建服务索引，调用方必须持有 s.mu。
func (s *Server) init() {
	s.services = make(map[Digest]*Service)
	for k, v := range s.config.Services {
		serviceDigest := common.CalSha256(k)
		s.services[Digest(serviceDigest)] = &Service{
			svcName:   k,
			svcType:   v.Type,
			token:     v.Token,
			bind_addr: v.BindAddr,
		}
	}
}

func (s *Server) Cancel() {
	s.muDone.Lock()
	defer s.muDone.Unlock()
	if !s.canceled {
		s.canceled = true
		s.cancel()
	}
}

// configSnapshot 在锁内拷贝一份配置，供 http 管理页读取。
func (s *Server) configSnapshot() (string, []ServiceView) {
	s.mu.Lock()
	defer s.mu.Unlock()

	views := make([]ServiceView, 0, len(s.config.Services))
	for k, v := range s.config.Services {
		views = append(views, ServiceView{
			Name:     k,
			Type:     v.Type,
			Token:    v.Token,
			BindAddr: v.BindAddr,
		})
	}
	return s.config.BindAddr, views
}

func (s *Server) authCredentials() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.AuthUsername, s.config.AuthPassword
}

func (s *Server) lookupService(serviceDigest string) (*Service, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, ok := s.services[Digest(serviceDigest)]
	return service, ok
}

func (s *Server) acceptLoop(sigChan chan os.Signal, updater chan *config.Config) {

	tcpAddr, err := net.ResolveTCPAddr("tcp", s.config.BindAddr)
	if err != nil {
		fmt.Println("server bind_addr invalid", s.config.BindAddr, err)
		return
	}
	l, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		fmt.Println("server listen fail", err)
		return
	}
	fmt.Println("server started listen", s.config.BindAddr)

	go func() {
		for {
			conn, err := l.AcceptTCP()
			if err != nil {
				fmt.Println("server accept fail", err)
				break
			}
			fmt.Println("server accept connection")
			go serveConnection(s, conn)
		}
	}()

label_for:
	for {
		select {
		case <-s.ctx.Done():
			fmt.Println("server receive s.ctx.Done()")
			break label_for

		case cc := <-sigChan:
			fmt.Println("server receive interrupt", cc)
			s.Cancel()

		case conf, ok := <-updater:
			if ok {
				fmt.Println("server receive update")
				s.hotUpdate(conf)
			}
		}
	}

	err = l.Close()
	if err != nil {
		fmt.Println("server close fail", err)
	}

	time.Sleep(10 * time.Millisecond)
	fmt.Println("server goto shutdown")
}

func serveConnection(s *Server, conn *net.TCPConn) {
	// 设置读取超时，防止恶意连接占用资源
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	// 读取完成后清除超时设置，后续处理根据需要自行设置
	conn.SetReadDeadline(time.Time{})

	if err != nil {
		fmt.Println("recv hello cmd fail", err)
		conn.Close()
		return
	}
	uri := req.URL.Path
	switch uri {
	case "/control/hello":
		do_control_channel_handshake(s, conn, reader, req)
	case "/data/hello":
		do_data_channel_handshake(s, conn, reader, req)
	default:
		do_httpapi(s, conn, req)
	}
}

func do_control_channel_handshake(s *Server, conn *net.TCPConn, reader *bufio.Reader, req *http.Request) {
	handshakeOk := false
	defer func() {
		// 握手没走完的连接在这里关掉；走完之后连接的所有权交给 ControlChannel。
		if !handshakeOk {
			conn.Close()
		}
	}()

	serviceDigest := req.Header.Get("service")
	if len(serviceDigest) <= 0 {
		fmt.Println("recv /control/hello service nil")
		return
	}
	service, ok := s.lookupService(serviceDigest)
	if !ok {
		fmt.Println("server can not find service", serviceDigest)
		resp := &http.Response{
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Status:     "404 not found",
			StatusCode: http.StatusNotFound,
			Header:     make(map[string][]string),
		}
		err := common.ResponseWriteWithBuffered(resp, conn)
		if err != nil {
			fmt.Println("response /control/hello fail", err)
		}
		return
	}

	nonce, err := common.RandString(16)
	if err != nil {
		fmt.Println("server generate nonce fail", err)
		return
	}

	resp := &http.Response{
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Header:     make(map[string][]string),
	}
	resp.Header.Set("nonce", nonce)
	err = common.ResponseWriteWithBuffered(resp, conn)
	if err != nil {
		fmt.Println("response /control/hello fail", err)
		return
	}

	req, err = http.ReadRequest(reader)
	if err != nil {
		fmt.Println("recv hello auth fail", err)
		return
	}
	if req.URL.Path != "/control/auth" {
		fmt.Println("recv hello auth fail, path incorrect", req.URL.Path)
		return
	}
	session_key := req.Header.Get("session_key")
	d := common.CalSha256(service.token + nonce)
	if subtle.ConstantTimeCompare([]byte(session_key), []byte(d)) != 1 {
		fmt.Println("recv hello auth invalid")
		resp = &http.Response{
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Status:     "401 Unauthorized",
			StatusCode: http.StatusUnauthorized,
			Header:     make(map[string][]string),
		}
		err = common.ResponseWriteWithBuffered(resp, conn)
		if err != nil {
			fmt.Println("response /control/auth fail", err)
		}
		return
	}
	fmt.Println("recv hello auth success")

	resp = &http.Response{
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Header:     make(map[string][]string),
	}
	err = common.ResponseWriteWithBuffered(resp, conn)
	if err != nil {
		fmt.Println("response /control/auth fail", err)
		return
	}

	handshakeOk = true

	cc := NewControlChannel(s.ctx, session_key, service, conn, reader)

	s.controlChannelManager.Put(serviceDigest, session_key, cc)

	// 必须按对象身份摘除，不能按 serviceDigest 删。
	// 客户端重连时新通道会用同一个 digest 注册进来，按 key 删会把刚建好的新通道干掉，
	// 表现为「重连第一次总是被静默掐断」。
	defer s.controlChannelManager.RemoveItem(cc)

	cc.Run()
}

func do_data_channel_handshake(s *Server, conn *net.TCPConn, reader *bufio.Reader, req *http.Request) {
	handedOver := false
	defer func() {
		if !handedOver {
			if err := conn.Close(); err != nil {
				fmt.Println("close connection fail", err)
			}
		}
	}()

	sessionKey := req.Header.Get("session_key")

	var respbuf [2]byte = [2]byte{0, 0}

	r := s.controlChannelManager.GetByKey2(sessionKey)
	if sessionKey == "" || r == nil {
		fmt.Println("server sessionKey invalid")
		if _, err := conn.Write(respbuf[:]); err != nil {
			fmt.Println("response /data/hello fail", err)
		}
		return
	}

	cc, ok := r.(*ControlChannel)
	if !ok {
		fmt.Println("runnable not controchannel")
		return
	}

	// 后续的数据包是直接从 conn 上裸读的，不走这个 bufio.Reader。
	// 如果握手请求之后还粘了数据在缓冲区里，裸读就会从错误的位置开始解析。
	if reader.Buffered() != 0 {
		fmt.Println("server /data/hello 握手后存在粘包数据，拒绝该连接", reader.Buffered())
		return
	}

	fmt.Println("server sessionKey ok")

	respbuf[0] = 1 // 1-OK, 0-FAIL

	switch cc.service.svcType {
	case config.TCP:
		respbuf[1] = 1
	case config.UDP:
		respbuf[1] = 2
	}
	_, err := conn.Write(respbuf[:])
	if err != nil {
		fmt.Println("response /data/hello fail", err)
		return
	}

	fmt.Println("response /data/hello success", respbuf)

	select {
	case <-s.ctx.Done():
	case <-cc.cancelCtx.Done():
	case cc.data_chan <- common.NewMyTcpConn(conn):
		handedOver = true
	}
}

func (s *Server) hotUpdate(newConfig *config.Config) {
	serverConfig := newConfig.Server

	s.mu.Lock()

	newServices := make([]string, 0, 6)
	delServices := make([]string, 0, 6)
	changedServices := make([]string, 0, 6)

	for svcName, newSvc := range serverConfig.Services {
		oldSvc, ok := s.config.Services[svcName]
		if !ok {
			newServices = append(newServices, svcName)
		} else if oldSvc != newSvc {
			// 名字没变但 bind_addr/token/type 变了。
			// 在跑的控制通道持有的是旧配置，不重建的话改动到重启才生效。
			changedServices = append(changedServices, svcName)
		}
	}

	for svcName := range s.config.Services {
		_, ok := serverConfig.Services[svcName]
		if !ok {
			delServices = append(delServices, svcName)
		}
	}

	fmt.Println("server hotUpdate newServices", newServices)
	fmt.Println("server hotUpdate delServices", delServices)
	fmt.Println("server hotUpdate changedServices", changedServices)

	s.config.Services = serverConfig.Services
	s.config.BindAddr = serverConfig.BindAddr
	s.config.AuthUsername = serverConfig.AuthUsername
	s.config.AuthPassword = serverConfig.AuthPassword

	s.init()

	s.mu.Unlock()

	// 关闭旧通道要在 s.mu 之外做：Close 会一直等到对方释放监听端口，
	// 持锁等待会把 http 管理页和握手一起卡住。
	for _, svcName := range append(delServices, changedServices...) {
		serviceDigest := common.CalSha256(svcName)
		s.controlChannelManager.RemoveByKey1(serviceDigest)
	}
}
