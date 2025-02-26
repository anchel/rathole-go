package server

import "github.com/anchel/rathole-go/internal/common"

type DatachannelConn struct {
	FinishChan    chan bool
	ClientTcpConn *common.MyTcpConn
}
