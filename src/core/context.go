package core

/**
 * context.go - proxy context
 *
 * @author Yaroslav Pogrebnyak <yyyaroslav@gmail.com>
 */

import "net"

type Context interface {
	String() string
	Ip() net.IP
	Port() int
	Sni() string
}

/**
 * Proxy tcp context
 */
type TcpContext struct {
	Hostname string
	/**
	 * Current client connection
	 */
	Conn net.Conn
}

func (t TcpContext) String() string {
	return t.Conn.RemoteAddr().String()
}

func (t TcpContext) Ip() net.IP {
	return t.Conn.RemoteAddr().(*net.TCPAddr).IP
}

func (t TcpContext) Port() int {
	return t.Conn.RemoteAddr().(*net.TCPAddr).Port
}

func (t TcpContext) Sni() string {
	return t.Hostname
}

/*
 * Proxy udp context
 */
type UdpContext struct {
	/**
	 * Current client remote address
	 */
	ClientAddr net.UDPAddr
}

func (u UdpContext) String() string {
	return u.ClientAddr.String()
}

func (u UdpContext) Ip() net.IP {
	return u.ClientAddr.IP
}

func (u UdpContext) Port() int {
	return u.ClientAddr.Port
}

func (u UdpContext) Sni() string {
	return ""
}

/*
 * Proxy VXLAN udp context
 */
type VXlanContext struct {
	/**
	 * Inside UDP packet the VXland IP/TCP packet is located
	 */
	ClientAddr net.UDPAddr
	//Hash       string
	Hash uint64
}

/*
 * Proxy Geneve udp context
 */
type GeneveContext struct {
	/**
	 * Inside UDP packet the Geneve IP/TCP packet is located
	 */
	ClientAddr net.UDPAddr
	//Hash       string
	Hash uint64
}

func (x VXlanContext) Ip() net.IP {
	return x.ClientAddr.IP
}

func (x VXlanContext) Port() int {
	return x.ClientAddr.Port
}

func (x VXlanContext) Sni() string {
	return ""
}

func (x VXlanContext) String() string {
	return x.ClientAddr.String()
}

func (x VXlanContext) hash() uint64 {
	return x.Hash
}

func (x GeneveContext) Ip() net.IP {
	return x.ClientAddr.IP
}

func (x GeneveContext) Port() int {
	return x.ClientAddr.Port
}

func (x GeneveContext) Sni() string {
	return ""
}

func (x GeneveContext) String() string {
	return x.ClientAddr.String()
}

func (x GeneveContext) hash() uint64 {
	return x.Hash
}
