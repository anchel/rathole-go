package common

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

// MaxPacketSize 是单个 udp 数据包载荷的上限，收发两端共用同一个值。
const MaxPacketSize = 8192

type MyTcpConn struct {
	*net.TCPConn
}

func NewMyTcpConn(c *net.TCPConn) *MyTcpConn {
	return &MyTcpConn{
		c,
	}
}

// WritePacket 把一个 udp 数据包按 [地址][2字节载荷长度][载荷] 的格式写进 tcp 流。
// 整帧一次性写出：分成多次写的话，前半帧写成功、后半帧失败就会在流里留下半个帧，
// 对端会一直按错位的边界解析下去。
func (mc *MyTcpConn) WritePacket(p []byte, addr *Address) (int, error) {
	if len(p) > MaxPacketSize {
		return 0, fmt.Errorf("MyTcpConn WritePacket, payload too large, %d > %d", len(p), MaxPacketSize)
	}

	buf := bytes.NewBuffer(make([]byte, 0, len(p)+32))
	if err := addr.WriteToWriter(buf); err != nil {
		return 0, fmt.Errorf("MyTcpConn WritePacket, addr write error, %w", err)
	}

	lenBuf := [2]byte{0, 0}
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(p)))
	buf.Write(lenBuf[:])
	buf.Write(p)

	return mc.Write(buf.Bytes())
}

func (mc *MyTcpConn) ReadPayload(p []byte) (int, error) {
	lenBuf := [2]byte{0, 0}
	if _, err := io.ReadFull(mc, lenBuf[:]); err != nil {
		return 0, fmt.Errorf("MyTcpConn ReadPayload read payload length error, %w", err)
	}
	length := int(binary.BigEndian.Uint16(lenBuf[:]))

	// 长度超过接收缓冲区，说明流已经错位或者对端不可信。
	// 这时既不能读也不能跳过（跳过等于按错误的边界继续解析），
	// 只能判定连接不可用，由调用方关闭。
	if length > len(p) {
		return 0, fmt.Errorf("MyTcpConn ReadPayload payload too large, %d > %d", length, len(p))
	}

	n, err := io.ReadFull(mc, p[:length])
	if err != nil {
		return n, fmt.Errorf("MyTcpConn ReadPayload read payload error, %w", err)
	}
	return n, nil
}

func (mc *MyTcpConn) ReadPacket(p []byte) (*Address, int, error) {
	addr := &Address{}
	err := addr.ReadFromReader(mc)
	if err != nil {
		return nil, 0, err
	}
	n, err := mc.ReadPayload(p)
	if err != nil {
		return nil, 0, err
	}
	return addr, n, nil
}

type AddressType byte

const (
	IPv4       AddressType = 1
	IPv6       AddressType = 2
	DomainName AddressType = 3
)

type Address struct {
	AddressType AddressType
	DomainName  string
	Port        int
	net.IP
	NetworkType string
}

func (a *Address) String() string {
	switch a.AddressType {
	case IPv4:
		return fmt.Sprintf("%s:%d", a.IP.String(), a.Port)
	case IPv6:
		return fmt.Sprintf("[%s]:%d", a.IP.String(), a.Port)
	case DomainName:
		return fmt.Sprintf("%s:%d", a.DomainName, a.Port)
	default:
		return "INVALID_ADDRESS_TYPE"
	}
}

func (a *Address) Network() string {
	return a.NetworkType
}

func (a *Address) ResolveIP() (net.IP, error) {
	if a.AddressType == IPv4 || a.AddressType == IPv6 {
		return a.IP, nil
	}
	if a.IP != nil {
		return a.IP, nil
	}
	addr, err := net.ResolveIPAddr("ip", a.DomainName)
	if err != nil {
		return nil, err
	}
	a.IP = addr.IP
	return addr.IP, nil
}

func NewAddressFromAddr(network string, addr string) (*Address, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseInt(portStr, 10, 32)
	if err != nil {
		return nil, errors.New("NewAddressFromAddr Port invalid")
	}
	return NewAddressFromHostPort(network, host, int(port)), nil
}

func NewAddressFromHostPort(network string, host string, port int) *Address {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return &Address{
				IP:          ip,
				Port:        port,
				AddressType: IPv4,
				NetworkType: network,
			}
		}
		return &Address{
			IP:          ip,
			Port:        port,
			AddressType: IPv6,
			NetworkType: network,
		}
	}
	return &Address{
		DomainName:  host,
		Port:        port,
		AddressType: DomainName,
		NetworkType: network,
	}
}

func (a *Address) ReadFromReader(r io.Reader) error {
	byteBuf := [1]byte{}
	_, err := io.ReadFull(r, byteBuf[:])
	if err != nil {
		return errors.New("unable to read ATYP" + err.Error())
	}
	a.AddressType = AddressType(byteBuf[0])
	switch a.AddressType {
	case IPv4:
		var buf [6]byte
		_, err := io.ReadFull(r, buf[:])
		if err != nil {
			return errors.New("failed to read IPv4")
		}
		a.IP = buf[0:4]
		a.Port = int(binary.BigEndian.Uint16(buf[4:6]))
	case IPv6:
		var buf [18]byte
		_, err := io.ReadFull(r, buf[:])
		if err != nil {
			return errors.New("failed to read IPv6")
		}
		a.IP = buf[0:16]
		a.Port = int(binary.BigEndian.Uint16(buf[16:18]))
	case DomainName:
		if _, err := io.ReadFull(r, byteBuf[:]); err != nil {
			return errors.New("failed to read domain name length")
		}
		// 这里必须先把长度转成 int 再参与运算。byteBuf[0] 是 byte，
		// 长度为 254/255 时 length+2 会回绕成 0/1，缓冲区被分配成 0/1 字节，
		// 后面 buf[0:length] 直接越界 panic（对端可远程触发）。
		length := int(byteBuf[0])
		if length == 0 {
			return errors.New("invalid domain name length")
		}
		buf := make([]byte, length+2)
		if _, err = io.ReadFull(r, buf); err != nil {
			return errors.New("failed to read domain name")
		}
		// the fucking browser uses IP as a domain name sometimes
		host := buf[0:length]
		if ip := net.ParseIP(string(host)); ip != nil {
			a.IP = ip
			if ip.To4() != nil {
				a.AddressType = IPv4
			} else {
				a.AddressType = IPv6
			}
		} else {
			a.DomainName = string(host)
		}
		a.Port = int(binary.BigEndian.Uint16(buf[length : length+2]))
	default:
		return errors.New("invalid atyp")
	}
	return nil
}

func (a *Address) WriteToWriter(w io.Writer) error {
	_, err := w.Write([]byte{byte(a.AddressType)})
	if err != nil {
		return err
	}
	switch a.AddressType {
	case DomainName:
		if len(a.DomainName) > 253 {
			return errors.New("domain name too long")
		}
		w.Write([]byte{byte(len(a.DomainName))})
		_, err = w.Write([]byte(a.DomainName))
	case IPv4:
		_, err = w.Write(a.IP.To4())
	case IPv6:
		_, err = w.Write(a.IP.To16())
	default:
		return errors.New("invalid atyp")
	}
	if err != nil {
		return err
	}
	port := [2]byte{}
	binary.BigEndian.PutUint16(port[:], uint16(a.Port))
	_, err = w.Write(port[:])
	return err
}

type UdpPacket struct {
	Payload []byte
	Addr    *Address
}
