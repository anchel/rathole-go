package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"sync"

	util "github.com/anchel/rathole-go/common"
	"github.com/anchel/rathole-go/config"
)

type Digest string

type Service struct {
	svcName   string
	svcType   config.ServiceType
	token     string
	bind_addr string
}

type Server struct {
	config            *config.ServerConfig
	services          map[Digest]*Service
	controlChannelMap map[Digest]*ControlChannel
	mu                sync.Mutex
}

func NewServer(c *config.ServerConfig) *Server {
	s := &Server{}
	s.config = c
	s.services = make(map[Digest]*Service)
	s.controlChannelMap = make(map[Digest]*ControlChannel)
	return s
}

func (s *Server) Run() {
	s.init()
	s.acceptLoop()
}

func (s *Server) init() {
	for k, v := range s.config.Services {
		digest := util.CalSha256(k)
		s.services[Digest(digest)] = &Service{
			svcName:   k,
			svcType:   v.Type,
			token:     v.Token,
			bind_addr: v.BindAddr,
		}
	}
}

func (s *Server) acceptLoop() {
	tcpAddr, _ := net.ResolveTCPAddr("tcp", s.config.BindAddr)
	l, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		fmt.Println("server listen fail", err)
		return
	}
	fmt.Println("server starting listen")
	for {
		conn, err := l.AcceptTCP()
		if err != nil {
			fmt.Println("server accept fail", err)
			break
		}
		fmt.Println("server accept connection")
		go serveConnection(s, conn)
	}

	fmt.Println("server goto shutdown")
}

func serveConnection(s *Server, conn *net.TCPConn) {
	// defer conn.Close()

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		fmt.Println("recv hello cmd fail", err)
		return
	}
	uri := req.URL.Path
	switch uri {
	case "/control/hello":
		do_control_channel_handshake(s, conn, req)
	case "/data/hello":
		do_data_channel_handshake(s, conn, req)
	}
}

func do_control_channel_handshake(s *Server, conn *net.TCPConn, req *http.Request) {
	digest := req.Header.Get("service")
	if len(digest) <= 0 {
		fmt.Println("recv /control/hello service nil")
		return
	}
	var service *Service
	var ok bool
	s.mu.Lock()
	service, ok = s.services[Digest(digest)]
	s.mu.Unlock()
	if !ok {
		fmt.Println("server can not find service", digest)
		resp := &http.Response{
			Status:     "404 not found",
			StatusCode: http.StatusNotFound,
			Header:     make(map[string][]string),
		}
		err := util.ResponseWriteWithBuffered(resp, conn)
		if err != nil {
			fmt.Println("response /control/hello fail", err)
		}
		return
	}
	nonce := util.RandStringRunes(16)
	fmt.Println("nonce", nonce)
	resp := &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Header:     make(map[string][]string),
	}
	resp.Header.Set("nonce", nonce)
	err := util.ResponseWriteWithBuffered(resp, conn)
	if err != nil {
		fmt.Println("response /control/hello fail", err)
		return
	}

	req, err = http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		fmt.Println("recv hello auth fail", err)
		return
	}
	if req.URL.Path != "/control/auth" {
		fmt.Println("recv hello auth fail, path incorrect", err)
		return
	}
	session_key := req.Header.Get("session_key")
	d := util.CalSha256(service.token + nonce)
	if session_key != d {
		fmt.Println("recv hello auth invalid", d, session_key)
		resp = &http.Response{
			Status:     "401 Unauthorized",
			StatusCode: http.StatusUnauthorized,
			Header:     make(map[string][]string),
		}
		err = util.ResponseWriteWithBuffered(resp, conn)
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
	err = util.ResponseWriteWithBuffered(resp, conn)
	if err != nil {
		fmt.Println("response /control/auth fail", err)
		return
	}

	cc := NewControlChannel(session_key, s.config, service)

	s.mu.Lock()
	c, ok := s.controlChannelMap[Digest(session_key)]
	if ok {
		c.Close()
		delete(s.controlChannelMap, Digest(session_key))
	}
	s.controlChannelMap[Digest(session_key)] = cc
	s.mu.Unlock()

	cc.Run(conn)
}

func do_data_channel_handshake(s *Server, conn *net.TCPConn, req *http.Request) {
	sessionKey := req.Header.Get("session_key")

	var cc *ControlChannel
	var ok bool
	s.mu.Lock()
	cc, ok = s.controlChannelMap[Digest(sessionKey)]
	s.mu.Unlock()

	var respbuf [2]byte
	if !ok {
		fmt.Println("server sessionKey invalid", sessionKey)
		// resp := &http.Response{
		// 	Status:     "401 Unauthorized",
		// 	StatusCode: http.StatusUnauthorized,
		// 	Header:     make(map[string][]string),
		// }
		// err := util.ResponseWriteWithBuffered(resp, conn)
		_, err := conn.Write(respbuf[:])
		if err != nil {
			fmt.Println("response /data/hello fail", err)
		}
		return
	}
	fmt.Println("server sessionKey ok", sessionKey)

	// resp := &http.Response{
	// 	Status:     "200 OK",
	// 	StatusCode: http.StatusOK,
	// 	Header:     make(map[string][]string),
	// }
	// resp.Header.Set("type", string(cc.service.svcType))
	// err := util.ResponseWriteWithBuffered(resp, conn)
	respbuf[0] = 1
	respbuf[1] = 0 // 1-tcp 2-udp
	if cc.service.svcType == config.TCP {
		respbuf[1] = 1
	} else if cc.service.svcType == config.UDP {
		respbuf[1] = 2
	}
	_, err := conn.Write(respbuf[:])
	if err != nil {
		fmt.Println("response /data/hello fail", err)
		return
	}

	fmt.Println("response /data/hello success", respbuf)

	cc.data_chan <- conn
}
