package server

import (
	"bufio"
	"context"
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

type Server struct {
	config                *config.ServerConfig
	services              map[Digest]*Service
	controlChannelManager *common.RunnableManager
	mu                    sync.Mutex
	cancelCtx             context.Context
	cancel                context.CancelFunc

	muDone   sync.Mutex
	canceled bool
}

func NewServer(c *config.ServerConfig) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{}
	s.config = c
	s.services = make(map[Digest]*Service)
	s.controlChannelManager = common.NewRunnableManager()
	s.cancelCtx = ctx
	s.cancel = cancel

	return s
}

func (s *Server) Run(sigChan chan os.Signal) {
	s.init()
	s.acceptLoop(sigChan)
}

func (s *Server) init() {
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

func (s *Server) acceptLoop(sigChan chan os.Signal) {

	go func() {
		tcpAddr, _ := net.ResolveTCPAddr("tcp", s.config.BindAddr)
		l, err := net.ListenTCP("tcp", tcpAddr)
		if err != nil {
			fmt.Println("server listen fail", err)
			return
		}
		fmt.Println("server started listen")
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
		case <-s.cancelCtx.Done():
			fmt.Println("server receive s.cancelCtx.Done()")
			break label_for
		case cc := <-sigChan:
			fmt.Println("server receive interrupt", cc)
			s.Cancel()
		}
	}
	time.Sleep(10 * time.Millisecond)
	fmt.Println("server goto shutdown")
}

func serveConnection(s *Server, conn *net.TCPConn) {
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		fmt.Println("recv hello cmd fail", err)
		return
	}
	uri := req.URL.Path
	switch uri {
	case "/control/hello":
		do_control_channel_handshake(s, conn, reader, req)
	case "/data/hello":
		do_data_channel_handshake(s, conn, req)
	}
}

func do_control_channel_handshake(s *Server, conn *net.TCPConn, reader *bufio.Reader, req *http.Request) {
	defer conn.Close()

	serviceDigest := req.Header.Get("service")
	if len(serviceDigest) <= 0 {
		fmt.Println("recv /control/hello service nil")
		return
	}
	var service *Service
	var ok bool
	s.mu.Lock()
	service, ok = s.services[Digest(serviceDigest)]
	s.mu.Unlock()
	if !ok {
		fmt.Println("server can not find service", serviceDigest)
		resp := &http.Response{
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
	nonce := common.RandStringRunes(16)
	// fmt.Println("nonce", nonce)
	resp := &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Header:     make(map[string][]string),
	}
	resp.Header.Set("nonce", nonce)
	err := common.ResponseWriteWithBuffered(resp, conn)
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
		fmt.Println("recv hello auth fail, path incorrect", err)
		return
	}
	session_key := req.Header.Get("session_key")
	d := common.CalSha256(service.token + nonce)
	if session_key != d {
		fmt.Println("recv hello auth invalid", d, session_key)
		resp = &http.Response{
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
	fmt.Println("recv hello auth success", session_key)

	resp = &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Header:     make(map[string][]string),
	}
	err = common.ResponseWriteWithBuffered(resp, conn)
	if err != nil {
		fmt.Println("response /control/auth fail", err)
		return
	}

	cc := NewControlChannel(s.cancelCtx, session_key, s.config, service, s, conn, reader)

	s.controlChannelManager.Put(serviceDigest, session_key, cc)

	cc.Run()
}

func do_data_channel_handshake(s *Server, conn *net.TCPConn, req *http.Request) {
	sessionKey := req.Header.Get("session_key")

	r := s.controlChannelManager.Get("", sessionKey)

	var respbuf [2]byte = [2]byte{0, 0}

	if r == nil {
		fmt.Println("server sessionKey invalid", sessionKey)
		_, err := conn.Write(respbuf[:])
		if err != nil {
			fmt.Println("response /data/hello fail", err)
		}
		return
	}

	fmt.Println("server sessionKey ok", sessionKey)

	cc, ok := r.(*ControlChannel)
	if !ok {
		fmt.Println("runnable not controchannel")
		return
	}

	respbuf[0] = 1
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
	case <-s.cancelCtx.Done():
	case <-cc.cancelCtx.Done():
	case cc.data_chan <- common.NewMyTcpConn(conn):
	}
}
