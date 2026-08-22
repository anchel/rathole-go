package server

import (
	"sync"
	"testing"
)

// 数据通道表会被速率监控和主循环两个 goroutine 同时访问。
// 裸 map 的并发读写会触发 fatal error: concurrent map read and map write，
// 这种错误 recover 拦不住，进程直接退出。
// 用 -race 跑这个用例可以锁住该修复。
func TestDatachannelMapConcurrentAccess(t *testing.T) {
	cc := &ControlChannel{
		datachannelConnMap: make(map[uint64]*DatachannelConn),
	}

	var wg sync.WaitGroup

	// 模拟主循环：不断新增/回收数据通道
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			cc.addDatachannel(nil)
			cc.finishOneDatachannel()
		}
	}()

	// 模拟速率监控：不断读取数量
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = cc.datachannelCount()
		}
	}()

	wg.Wait()
}

// Finish 会被读、写两侧的 goroutine 以及回收逻辑同时调用，必须可重入。
func TestDatachannelConnFinishIsIdempotent(t *testing.T) {
	dc := NewDatachannelConn(1, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dc.Finish()
		}()
	}
	wg.Wait()

	select {
	case <-dc.FinishChan:
	default:
		t.Fatal("FinishChan 应当已经关闭")
	}
}

func TestFinishAllDatachannels(t *testing.T) {
	cc := &ControlChannel{
		datachannelConnMap: make(map[uint64]*DatachannelConn),
	}

	dcs := make([]*DatachannelConn, 0, 5)
	for i := 0; i < 5; i++ {
		dcs = append(dcs, cc.addDatachannel(nil))
	}

	cc.finishAllDatachannels()

	if cc.datachannelCount() != 0 {
		t.Fatalf("数据通道表应当被清空, len=%d", cc.datachannelCount())
	}
	for _, dc := range dcs {
		select {
		case <-dc.FinishChan:
		default:
			t.Fatalf("数据通道 %d 应当收到结束通知", dc.id)
		}
	}
}
