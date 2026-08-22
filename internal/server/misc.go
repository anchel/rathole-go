package server

import (
	"sync"

	"github.com/anchel/rathole-go/internal/common"
)

type DatachannelConn struct {
	id            uint64
	FinishChan    chan struct{}
	ClientTcpConn *common.MyTcpConn

	finishOnce sync.Once
}

func NewDatachannelConn(id uint64, conn *common.MyTcpConn) *DatachannelConn {
	return &DatachannelConn{
		id:            id,
		FinishChan:    make(chan struct{}),
		ClientTcpConn: conn,
	}
}

// Finish 通知所有使用方该数据通道已结束。
// 用 sync.Once 包住，读写两侧的 goroutine 出错时都能直接调用而不必担心重复 close 引发 panic。
func (dc *DatachannelConn) Finish() {
	dc.finishOnce.Do(func() {
		close(dc.FinishChan)
	})
}
