package common

import (
	"bytes"
	"testing"
)

// 域名长度字段是 byte，长度为 254/255 时 length+2 会回绕，
// 缓冲区被分配成 0/1 字节，后面的切片操作直接越界 panic。
// 这个报文完全由对端控制，等于一个远程可触发的进程崩溃点。
func TestAddressReadFromReaderDomainLengthNeverPanics(t *testing.T) {
	for length := 0; length <= 255; length++ {
		buf := bytes.NewBuffer(nil)
		buf.WriteByte(byte(DomainName))
		buf.WriteByte(byte(length))
		buf.Write(make([]byte, 300)) // 后续字节给足，确保走到解析逻辑而不是提前 EOF

		a := &Address{}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("域名长度为 %d 时发生 panic: %v", length, r)
				}
			}()
			_ = a.ReadFromReader(buf)
		}()
	}
}

func TestAddressReadFromReaderRejectsInvalidType(t *testing.T) {
	buf := bytes.NewBuffer([]byte{99, 0, 0, 0, 0})
	a := &Address{}
	if err := a.ReadFromReader(buf); err == nil {
		t.Fatal("非法的地址类型应当返回错误")
	}
}

func TestAddressRoundTrip(t *testing.T) {
	cases := []*Address{
		NewAddressFromHostPort("udp", "127.0.0.1", 8080),
		NewAddressFromHostPort("udp", "::1", 53),
		NewAddressFromHostPort("udp", "example.com", 65535),
	}

	for _, want := range cases {
		buf := bytes.NewBuffer(nil)
		if err := want.WriteToWriter(buf); err != nil {
			t.Fatalf("WriteToWriter(%s) 失败: %v", want.String(), err)
		}

		got := &Address{}
		if err := got.ReadFromReader(buf); err != nil {
			t.Fatalf("ReadFromReader(%s) 失败: %v", want.String(), err)
		}
		got.NetworkType = want.NetworkType

		if got.String() != want.String() {
			t.Fatalf("往返后地址不一致, got %s, want %s", got.String(), want.String())
		}
	}
}

// 载荷长度超过接收缓冲区时必须报错，而且不能读走任何载荷字节，
// 否则流会按错误的边界继续解析下去。
func TestReadPayloadRejectsOversizedLength(t *testing.T) {
	if MaxPacketSize >= 65535 {
		t.Skip("MaxPacketSize 已经等于长度字段上限，构造不出超长报文")
	}

	server, client := tcpPipe(t)
	defer server.Close()
	defer client.Close()

	go func() {
		// 声明一个远大于接收缓冲区的长度
		client.Write([]byte{0xFF, 0xFF})
		client.Write(make([]byte, 100))
	}()

	p := make([]byte, MaxPacketSize)
	n, err := server.ReadPayload(p)
	if err == nil {
		t.Fatal("载荷长度超过缓冲区时应当返回错误")
	}
	if n != 0 {
		t.Fatalf("出错时不应报告读到了载荷, got %d", n)
	}
}

func TestWritePacketRejectsOversizedPayload(t *testing.T) {
	server, client := tcpPipe(t)
	defer server.Close()
	defer client.Close()

	addr := NewAddressFromHostPort("udp", "127.0.0.1", 1234)
	if _, err := client.WritePacket(make([]byte, MaxPacketSize+1), addr); err == nil {
		t.Fatal("超长载荷应当被拒绝")
	}
}

func TestPacketRoundTrip(t *testing.T) {
	server, client := tcpPipe(t)
	defer server.Close()
	defer client.Close()

	want := []byte("hello rathole")
	wantAddr := NewAddressFromHostPort("udp", "192.168.1.7", 5353)

	go func() {
		if _, err := client.WritePacket(want, wantAddr); err != nil {
			t.Error("WritePacket 失败:", err)
		}
	}()

	p := make([]byte, MaxPacketSize)
	gotAddr, n, err := server.ReadPacket(p)
	if err != nil {
		t.Fatal("ReadPacket 失败:", err)
	}
	if !bytes.Equal(p[:n], want) {
		t.Fatalf("载荷不一致, got %q, want %q", p[:n], want)
	}
	if gotAddr.String() != wantAddr.String() {
		t.Fatalf("地址不一致, got %s, want %s", gotAddr.String(), wantAddr.String())
	}
}
