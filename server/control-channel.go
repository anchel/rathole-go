package server

import (
	"net"

	"github.com/anchel/rathole-go/config"
)

type ControlChannel struct {
	session_key  string
	clientConfig *config.ServerConfig
	service      *Service
}

func NewControlChannel(session_key string, conf *config.ServerConfig, service *Service) *ControlChannel {
	cc := &ControlChannel{
		session_key:  session_key,
		clientConfig: conf,
		service:      service,
	}

	return cc
}

func (cc *ControlChannel) Close() {
	// todo
}

func (cc *ControlChannel) Run(conn net.Conn) {

}
