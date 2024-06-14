package common

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
)

type RewindReader struct {
	mu         sync.Mutex
	rawReader  io.Reader
	buf        []byte
	bufReadIdx int
	rewound    bool
	buffering  bool
	bufferSize int
}

func (r *RewindReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Println("RewindReader Read", len(p), r.rewound, len(r.buf), r.bufReadIdx)

	if r.rewound {
		if len(r.buf) > r.bufReadIdx {
			n := copy(p, r.buf[r.bufReadIdx:])
			r.bufReadIdx += n
			return n, nil
		}
		r.rewound = false // all buffering content has been read
	}
	n, err := r.rawReader.Read(p)
	if r.buffering {
		r.buf = append(r.buf, p[:n]...)
		if len(r.buf) > r.bufferSize*2 {
			fmt.Println("read too many bytes!")
		}
		fmt.Println("RewindReader Read buffering", len(r.buf), n, string(r.buf))
	}
	return n, err
}

func (r *RewindReader) ReadByte() (byte, error) {
	buf := [1]byte{}
	_, err := r.Read(buf[:])
	return buf[0], err
}

func (r *RewindReader) Discard(n int) (int, error) {
	buf := [128]byte{}
	if n < 128 {
		return r.Read(buf[:n])
	}
	for discarded := 0; discarded+128 < n; discarded += 128 {
		_, err := r.Read(buf[:])
		if err != nil {
			return discarded, err
		}
	}
	if rest := n % 128; rest != 0 {
		return r.Read(buf[:rest])
	}
	return n, nil
}

func (r *RewindReader) Rewind() {
	r.mu.Lock()
	if r.bufferSize == 0 {
		panic("no buffer")
	}
	r.rewound = true
	r.bufReadIdx = 0
	r.mu.Unlock()
}

func (r *RewindReader) StopBuffering() {
	r.mu.Lock()
	r.buffering = false
	r.mu.Unlock()
}

func (r *RewindReader) SetBufferSize(size int) {
	r.mu.Lock()
	if size == 0 { // disable buffering
		if !r.buffering {
			panic("reader is disabled")
		}
		r.buffering = false
		r.buf = nil
		r.bufReadIdx = 0
		r.bufferSize = 0
	} else {
		if r.buffering {
			panic("reader is buffering")
		}
		r.buffering = true
		r.bufReadIdx = 0
		r.bufferSize = size
		r.buf = make([]byte, 0, size)
	}
	r.mu.Unlock()
}

type RewindConn struct {
	net.Conn
	*RewindReader
}

func (c *RewindConn) Read(p []byte) (int, error) {
	return c.RewindReader.Read(p)
}

func NewRewindConn(conn net.Conn) *RewindConn {
	return &RewindConn{
		Conn: conn,
		RewindReader: &RewindReader{
			rawReader: conn,
		},
	}
}

type MyTcpConn struct {
	*net.TCPConn
}

func NewMyTcpConn(c *net.TCPConn) *MyTcpConn {
	return &MyTcpConn{
		c,
	}
}

var index int

func (mc *MyTcpConn) Read(p []byte) (int, error) {
	n, e := mc.TCPConn.Read(p)
	// fmt.Println("MyConn Read", n, e)
	return n, e
}

func (mc *MyTcpConn) WritePacket(p []byte, addr *Address) (int, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 2))
	err := addr.WriteToWriter(buf)
	if err != nil {
		return 0, fmt.Errorf("MyTcpConn WritePacket, addr write error, %s", err.Error())
	}

	lenBuf := [2]byte{0, 0}
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(p)))
	n, err := buf.Write(lenBuf[:])
	if err != nil {
		return 0, fmt.Errorf("MyTcpConn WritePacket, p length write error, %s, %d", err.Error(), n)
	}
	// fmt.Println("MyTcpConn WritePacket", len(buf.Bytes()), buf.Bytes())
	n, err = mc.Write(buf.Bytes())
	if err != nil {
		return 0, fmt.Errorf("MyTcpConn WritePacket, header write error, %s, %d", err.Error(), n)
	}
	return mc.Write(p)
}

func (mc *MyTcpConn) ReadPayload(p []byte) (int, error) {
	lenBuf := [2]byte{0, 0}
	n, err := io.ReadFull(mc, lenBuf[:])
	if err != nil {
		return n, fmt.Errorf("MyTcpConn ReadPayload read payload length error, %s, %d", err, n)
	}
	length := binary.BigEndian.Uint16(lenBuf[:])

	if len(p) < int(length) {
		return n, fmt.Errorf("MyTcpConn ReadPayload length of p is not enough, %s, %d", err, n)
	}

	n, err = io.ReadFull(mc, p[:length])
	if err != nil || n != int(length) {
		return n, fmt.Errorf("MyTcpConn ReadPayload read payload error, %s, %d", err, n)
	}
	return n, err
}

func (mc *MyTcpConn) ReadPacket(p []byte) (*Address, int, error) {
	addr := &Address{}
	err := addr.ReadFromReader(mc)
	if err != nil {
		return nil, 0, err
	}
	// fmt.Println("MyTcpConn ReadPacket addr", addr.AddressType, addr.NetworkType, addr.IP, addr.Port, addr)
	n, err := mc.ReadPayload(p)
	if err != nil {
		fmt.Println("MyTcpConn ReadPacket ReadPayload error", err)
		return nil, 0, err
	}
	// fmt.Println("MyTcpConn ReadPacket finish")
	return addr, n, err
}

type Command byte

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
		return errors.New("unable to read ATYP")
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
		_, err := io.ReadFull(r, byteBuf[:])
		length := byteBuf[0]
		if err != nil {
			return errors.New("failed to read domain name length")
		}
		buf := make([]byte, length+2)
		_, err = io.ReadFull(r, buf)
		if err != nil {
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

type Metadata struct {
	Command
	*Address
}

func (r *Metadata) ReadFromReader(rr io.Reader) error {
	byteBuf := [1]byte{}
	_, err := io.ReadFull(rr, byteBuf[:])
	if err != nil {
		return err
	}
	r.Command = Command(byteBuf[0])
	r.Address = new(Address)
	err = r.Address.ReadFromReader(rr)
	if err != nil {
		return err
	}
	return nil
}

func (r *Metadata) WriteToWriter(w io.Writer) error {
	buf := bytes.NewBuffer(make([]byte, 0, 64))
	buf.WriteByte(byte(r.Command))
	if err := r.Address.WriteToWriter(buf); err != nil {
		return err
	}
	// use tcp by default
	r.Address.NetworkType = "tcp"
	_, err := w.Write(buf.Bytes())
	return err
}

func (r *Metadata) Network() string {
	return r.Address.Network()
}

func (r *Metadata) String() string {
	return r.Address.String()
}

func WriteFile(p []byte) {
	index++
	fileName := fmt.Sprintf("local/data/data-%d.txt", index)
	os.WriteFile(fileName, p, 0666)
}

type UdpPacket struct {
	Payload []byte
	Addr    *Address
}
