package common

import (
	"net"
	"testing"
)

// tcpPipe 建一对真实的 tcp 连接，返回 (服务端, 客户端)。
func tcpPipe(t *testing.T) (*MyTcpConn, *MyTcpConn) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("监听失败:", err)
	}
	defer l.Close()

	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := l.Accept()
		ch <- result{c, err}
	}()

	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal("连接失败:", err)
	}

	r := <-ch
	if r.err != nil {
		t.Fatal("accept 失败:", r.err)
	}

	return NewMyTcpConn(r.conn.(*net.TCPConn)), NewMyTcpConn(client.(*net.TCPConn))
}
