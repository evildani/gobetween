package udp

/**
 * server.go - UDP server implementation
 *
 * @author Illarion Kovalchuk <illarion.kovalchuk@gmail.com>
 */

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eric-lindau/udpfacade"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/notional-labs/gobetween/src/balance"
	"github.com/notional-labs/gobetween/src/config"
	"github.com/notional-labs/gobetween/src/core"
	"github.com/notional-labs/gobetween/src/discovery"
	"github.com/notional-labs/gobetween/src/healthcheck"
	"github.com/notional-labs/gobetween/src/logging"
	"github.com/notional-labs/gobetween/src/server/modules/access"
	"github.com/notional-labs/gobetween/src/server/scheduler"
	"github.com/notional-labs/gobetween/src/server/udp/session"
	"github.com/notional-labs/gobetween/src/stats"
	"github.com/notional-labs/gobetween/src/utils"
)

const (
	UDP_PACKET_SIZE = 65507
	CLEANUP_EVERY   = time.Second * 1
)

var log = logging.For("udp/server")

/**
 * UDP server implementation
 */
type Server struct {
	/* Server name */
	name string

	/* Server configuration */
	cfg config.Server

	/* Scheduler */
	scheduler *scheduler.Scheduler

	/* Server connection */
	serverConn *net.UDPConn

	/* Flag indicating that server is stopped */
	stopped uint32

	/* Stop channel */
	stop chan bool

	/* ----- modules ----- */

	/* Access module checks if client is allowed to connect */
	access *access.Access

	/* ----- sessions ----- */
	//sessions map[string]*session.Session
	sessions map[uint64]*session.Session
	mu       sync.Mutex
}

type VXLANHdr struct {
	Ip  layers.IPv4
	Tcp layers.TCP
}

type GeneveHdr struct {
	Ip  layers.IPv4
	Tcp layers.TCP
}

type GeneveOption struct {
	Class  uint16 // 16 bits
	Type   uint8  // 8 bits
	Flags  uint8  // 3 bits
	Length uint8  // 5 bits
	Data   []byte
}

/**
 * fire'n'forget connection pool
 */
type connPool struct {
	pool map[string]net.Conn
	mu   sync.RWMutex
}

func newConnPool() *connPool {
	return &connPool{
		pool: make(map[string]net.Conn),
	}
}

func (cp *connPool) get(addr string) net.Conn {
	if cp == nil {
		return nil
	}

	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.pool[addr]
}

func (cp *connPool) put(addr string, conn net.Conn) {
	if cp == nil {
		return
	}

	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.pool[addr] = conn
}

func (cp *connPool) close() {
	if cp == nil {
		return
	}

	cp.mu.Lock()
	defer cp.mu.Unlock()

	for _, conn := range cp.pool {
		conn.Close()
	}

	cp.pool = nil
}

/**
 * Creates new UDP server
 */
func New(name string, cfg config.Server) (*Server, error) {
	//fmt.Println("Server New()")
	statsHandler := stats.NewHandler(name)
	scheduler := &scheduler.Scheduler{
		Balancer:     balance.New(nil, cfg.Balance),
		Discovery:    discovery.New(cfg.Discovery.Kind, *cfg.Discovery),
		Healthcheck:  healthcheck.New(cfg.Healthcheck.Kind, *cfg.Healthcheck),
		StatsHandler: statsHandler,
	}

	server := &Server{
		name:      name,
		cfg:       cfg,
		scheduler: scheduler,
		stop:      make(chan bool),
		//sessions:  make(map[string]*session.Session),
		sessions: make(map[uint64]*session.Session),
	}

	/* Add access if needed */
	if cfg.Access != nil {
		access, err := access.NewAccess(cfg.Access)
		if err != nil {
			return nil, fmt.Errorf("Could not initialize access restrictions: %v", err)
		}
		server.access = access
	}

	log.Info("Creating UDP server '", name, "': ", cfg.Bind, " ", cfg.Balance, " ", cfg.Discovery.Kind, " ", cfg.Healthcheck.Kind)
	return server, nil
}

/**
 * Returns current server configuration
 */
func (this *Server) Cfg() config.Server {
	return this.cfg
}

/**
 * Starts server
 */
func (this *Server) Start() error {
	//fmt.Println("Server Start()")
	// Start listening
	if err := this.listen(); err != nil {
		return fmt.Errorf("Could not start listening UDP: %v", err)
	}

	this.scheduler.StatsHandler.Start()
	this.scheduler.Start()
	this.serve()

	go func() {
		ticker := time.NewTicker(CLEANUP_EVERY)
		for {
			select {
			case <-ticker.C:
				fmt.Println("Current Session Count: ", len(this.sessions))
				// this.mu.Lock()
				// for k, s := range this.sessions {
				// 	fmt.Println("Session key: ", k)
				// 	fmt.Println("Session backend: ", s.GetBackEnd())
				// }
				// this.mu.Unlock()
				this.cleanup()
				/* handle server stop */
			case <-this.stop:
				log.Info("Stopping ", this.name)
				atomic.StoreUint32(&this.stopped, 1)

				ticker.Stop()

				this.serverConn.Close()

				this.scheduler.StatsHandler.Stop()
				this.scheduler.Stop()

				this.mu.Lock()
				for k, s := range this.sessions {
					delete(this.sessions, k)
					s.Close()
				}
				this.mu.Unlock()

				return
			}
		}
	}()

	return nil
}

/**
 * Start accepting connections
 */
func (this *Server) listen() error {
	listenAddr, err := net.ResolveUDPAddr("udp", this.cfg.Bind)
	if err != nil {
		return fmt.Errorf("Failed to resolve udp address %v : %v", this.cfg.Bind, err)
	}
	fmt.Println("UDP Listen on: ", this.cfg.Bind, listenAddr)
	this.serverConn, err = net.ListenUDP("udp", listenAddr)

	if err != nil {
		return fmt.Errorf("Failed to create listening udp socket: %v", err)
	}

	return nil
}

/**
 * Start serving
 */
func (this *Server) serve() {
	cfg := session.Config{}
	if this.cfg.Vxlan != nil {
		fmt.Println("ClientIdleTimeout", *this.cfg.ClientIdleTimeout)
		fmt.Println("BackendIdleTimeout", *this.cfg.BackendIdleTimeout)
		fmt.Println("Config: ", this.cfg.Vxlan)
		c_timeout, _ := strconv.ParseInt(*this.cfg.ClientIdleTimeout, 10, 32)
		s_timeout, _ := strconv.ParseInt(*this.cfg.BackendIdleTimeout, 10, 32)
		cfg = session.Config{
			MaxRequests:        this.cfg.Vxlan.MaxRequests,
			MaxResponses:       this.cfg.Vxlan.MaxResponses,
			ClientIdleTimeout:  time.Duration(int(c_timeout)),
			BackendIdleTimeout: time.Duration(s_timeout),
			Transparent:        this.cfg.Vxlan.Transparent,
		}
	} else if this.cfg.Geneve != nil {
		fmt.Println("ClientIdleTimeout", *this.cfg.ClientIdleTimeout)
		fmt.Println("BackendIdleTimeout", *this.cfg.BackendIdleTimeout)
		fmt.Println("Config: ", this.cfg.Geneve)
		c_timeout, _ := strconv.ParseInt(*this.cfg.ClientIdleTimeout, 10, 32)
		s_timeout, _ := strconv.ParseInt(*this.cfg.BackendIdleTimeout, 10, 32)
		cfg = session.Config{
			MaxRequests:        this.cfg.Geneve.MaxRequests,
			MaxResponses:       this.cfg.Geneve.MaxResponses,
			ClientIdleTimeout:  time.Duration(int(c_timeout)),
			BackendIdleTimeout: time.Duration(s_timeout),
			Transparent:        this.cfg.Geneve.Transparent,
		}
	} else {
		cfg = session.Config{
			MaxRequests:        this.cfg.Udp.MaxRequests,
			MaxResponses:       this.cfg.Udp.MaxResponses,
			ClientIdleTimeout:  utils.ParseDurationOrDefault(*this.cfg.ClientIdleTimeout, 0),
			BackendIdleTimeout: utils.ParseDurationOrDefault(*this.cfg.BackendIdleTimeout, 0),
			Transparent:        this.cfg.Udp.Transparent,
		}
	}
	var cp *connPool
	if cfg.MaxRequests == 1 {
		cp = newConnPool()
	}
	fmt.Println("Start Listen GO Routine")
	// Main loop goroutine - reads incoming data and decides what to do
	go func() {
		defer cp.close()
		buf := make([]byte, 65507)
		for {
			n, clientAddr, err := this.serverConn.ReadFromUDP(buf)
			if err != nil {
				fmt.Println(err)
				continue
			}
			fmt.Println("UDP packet size: %d %d", n, len(buf))
			vxlanHdr := VXLANHdr{}
			if this.cfg.Vxlan != nil {
				log.Debug("UPD Client Source Addess: ", clientAddr.String())
				//payload := make([]byte, UDP_PACKET_SIZE)
				data := make([]byte, UDP_PACKET_SIZE)
				//n, oobn, falgs, clientAddr, err :=  this.serverConn.ReadMsgUDP(payload, oob_data)
				// gopacket.DecodeFunc(decodeVXLAN)
				// layers.LayerTypeUDP
				packet := gopacket.NewPacket(buf, layers.LayerTypeVXLAN, gopacket.NoCopy)
				vxlan := packet.Layer(layers.LayerTypeVXLAN)
				//log.Debug("Detected VXLAN packet")
				payload := vxlan.LayerPayload()
				packet = gopacket.NewPacket(payload, layers.LayerTypeEthernet, gopacket.NoCopy)
				ipv4Layer := packet.Layer(layers.LayerTypeIPv4)
				ipv4 := layers.IPv4{
					Version:  ipv4Layer.(*layers.IPv4).Version,
					SrcIP:    ipv4Layer.(*layers.IPv4).DstIP,
					DstIP:    ipv4Layer.(*layers.IPv4).SrcIP,
					TTL:      ipv4Layer.(*layers.IPv4).TTL,
					Id:       ipv4Layer.(*layers.IPv4).Id,
					Protocol: ipv4Layer.(*layers.IPv4).Protocol,
				}
				//log.Debug("Detected IP packet: ", ipv4.SrcIP.String(), ipv4.DstIP.String(), ipv4.Id, ipv4.Protocol.String())
				tcpLayer := packet.Layer(layers.LayerTypeTCP)
				tcp := layers.TCP{
					SrcPort: tcpLayer.(*layers.TCP).DstPort,
					DstPort: tcpLayer.(*layers.TCP).SrcPort,
					PSH:     tcpLayer.(*layers.TCP).PSH,
					ACK:     tcpLayer.(*layers.TCP).ACK,
					FIN:     tcpLayer.(*layers.TCP).FIN,
					Seq:     tcpLayer.(*layers.TCP).Ack,
					Ack:     tcpLayer.(*layers.TCP).Seq + uint32(len(data)),
					Window:  tcpLayer.(*layers.TCP).Window,
				}
				//log.Debug("Detected TCP packet: ", tcp.SrcPort, tcp.DstPort, uint32(len(data)))
				vxlanHdr = VXLANHdr{Ip: ipv4, Tcp: tcp}
			} else if this.cfg.Geneve != nil {
				//A geneve payload would be:
				// geneve headers
				// ethernet
				// ip
				// udp
				// vxlan
				// ethernet
				// ip
				// tcp
				log.Debug("Geneve UPD Client Source Addess: ", clientAddr.String())
				//var geneve //layers.Geneve
				//var payload []byte
				// fmt.Println("Size of data: ", len(data))
				// fmt.Println("Geneve version: %c", data[0]>>7)
				options_len := (buf[0] & 0x3f) * 4
				//fmt.Println("Geneve Options len %d: ", options_len)
				geneve_proto := layers.EthernetType(binary.BigEndian.Uint16(buf[2:4]))
				//fmt.Println("Geneve Protocol %d: ", geneve_proto)
				if geneve_proto != 0x0800 {
					fmt.Println("Geneve does not contain IPv4")
					continue
				}
				//geneve_vni := binary.BigEndian.Uint32(data[4:7])
				//fmt.Println("Geneve VNI %d ", geneve_vni)
				offset, length := uint8(8), int32(options_len)
				//fmt.Println("offset, length: ", offset, length)
				for length > 0 {
					opt, len, _ := decodeGeneveOption(buf[offset:])
					_ = opt
					//fmt.Println("Geneve Opt: %x ", opt.Class)
					length -= int32(len)
					offset += len
				}
				//remove Geneve header.
				//fmt.Println("Off set should be 8 + options_len:", int32(options_len)+8)
				//fmt.Println("New offset (40) is: ", offset)
				//start ethernet header
				// AWS Geneve does not have ethernet frame

				//IPv4 header 20 bytes + options
				// g_dstIP1 := buf[offset+16]
				// g_dstIP2 := buf[offset+17]
				// g_dstIP3 := buf[offset+18]
				// g_dstIP4 := buf[offset+19]
				// g_dstip := net.IPv4(g_dstIP1, g_dstIP2, g_dstIP3, g_dstIP4)
				//log.Debug("Geneve Dst IP: ", g_dstip.String())
				offset = offset + 20
				//UDP
				//g_destport := binary.BigEndian.Uint16(buf[offset+2 : offset+4])
				//log.Debug("Dest Port (4789): ", g_destport)
				offset = offset + 8
				//VXLAN
				vxlan_offset := offset
				// var vxlan_buf [4]byte                       //24 bytes needed asking for 32
				// copy(vxlan_buf[1:], buf[offset+4:offset+7]) //skip first byte making it 24
				// fmt.Println("VXLan ID: ", binary.BigEndian.Uint32(vxlan_buf[:]))
				// fmt.Println("VXLan Group Pol ID: ", binary.BigEndian.Uint16(buf[offset+2:offset+4]))
				offset = offset + 8
				//ethernet inside VxLAN
				offset = offset + 14
				// IP inside VXLAN
				vxlan_protocol_ip := layers.IPProtocol(buf[offset+9])
				if vxlan_protocol_ip != layers.IPProtocolTCP {
					fmt.Println("Incomming VXLAN packet does not contain a TCP header ")
					continue
				}
				srcIP1 := buf[offset+12]
				srcIP2 := buf[offset+13]
				srcIP3 := buf[offset+14]
				srcIP4 := buf[offset+15]
				dstIP1 := buf[offset+16]
				dstIP2 := buf[offset+17]
				dstIP3 := buf[offset+18]
				dstIP4 := buf[offset+19]
				srcip := net.IPv4(srcIP1, srcIP2, srcIP3, srcIP4)
				dstip := net.IPv4(dstIP1, dstIP2, dstIP3, dstIP4)
				log.Debug("VXLAN Src IP:", srcip.String())
				log.Debug("VXLAN Dst IP:", dstip.String())
				ipv4 := layers.IPv4{
					SrcIP: srcip,
					DstIP: dstip,
				}
				offset = offset + 20
				//TCP inside VXLAND
				b_srcport := binary.BigEndian.Uint16(buf[offset : offset+2])
				//log.Debug("VXLAN TCP: ", b_srcport)
				b_dstport := binary.BigEndian.Uint16(buf[offset+2 : offset+4])
				//flog.Debug("VXLAN TCP: ", b_dstport)
				srcport := layers.TCPPort(b_srcport)
				dstport := layers.TCPPort(b_dstport)
				tcp := layers.TCP{
					SrcPort: srcport,
					DstPort: dstport,
				}
				log.Debug("Detected TCP packet: ", tcp.SrcPort, tcp.DstPort, uint32(len(buf)))
				vxlanHdr = VXLANHdr{Ip: ipv4, Tcp: tcp}
				offset = offset + 32
				//remove up to VXLAN Header
				buf = buf[vxlan_offset:]
			}
			if err != nil {
				if atomic.LoadUint32(&this.stopped) == 1 {
					return
				}

				log.Error("Failed to read from UDP: ", err)

				continue
			}

			if this.access != nil {
				if !this.access.Allows(&clientAddr.IP) {
					log.Debug("Client disallowed to connect: ", clientAddr.IP)
					continue
				}
			}

			// special case for single request mode
			if cfg.MaxRequests == 1 {
				err := this.fireAndForget(cp, clientAddr, buf[:n])
				if err != nil {
					log.Errorf("Error sending data to backend: %v ", err)
				}

				continue
			}
			if this.cfg.Vxlan != nil {
				this.proxyVxlan(cfg, vxlanHdr, buf[:n])
			} else if this.cfg.Geneve != nil {
				//buf has its packet cut to geneve paylaod
				this.proxyGeneve(cfg, vxlanHdr, buf[:n])
			} else {
				this.proxy(cfg, clientAddr, buf[:n])
			}

		}
	}()
}

/**
 * Safely remove connections that have marked themself as done
 */
func (this *Server) cleanup() {
	//fmt.Println("Clean Up")
	this.mu.Lock()
	defer this.mu.Unlock()
	for k, s := range this.sessions {
		//fmt.Println("Session key: ", k)
		//fmt.Println("Session key: ", s)
		if s.IsDone() {
			//fmt.Println("Delete Session: ", k)
			delete(this.sessions, k)
		}
	}

	this.scheduler.StatsHandler.Connections <- uint(len(this.sessions))
}

/**
 * Elect and connect to backend
 */
func (this *Server) electAndConnect(pool *connPool, clientAddr *net.UDPAddr) (net.Conn, *core.Backend, error) {
	backend, err := this.scheduler.TakeBackend(core.UdpContext{
		ClientAddr: *clientAddr,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("Could not elect backend for clientAddr %v: %v", clientAddr, err)
	}

	host := backend.Host
	port := backend.Port

	addrStr := host + ":" + port

	conn := pool.get(addrStr)
	if conn != nil {
		return conn, backend, nil
	}

	addr, err := net.ResolveUDPAddr("udp", addrStr)
	if err != nil {
		return nil, nil, fmt.Errorf("Could not resolve udp address %s: %v", addrStr, err)
	}

	if this.cfg.Udp.Transparent {
		conn, err = udpfacade.DialUDPFrom(clientAddr, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not dial UDP addr %v from %v: %v", addr, clientAddr, err)
		}
	} else {
		conn, err = net.DialUDP("udp", nil, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not dial UDP addr %v: %v", addr, err)
		}
	}

	pool.put(addrStr, conn)

	return conn, backend, nil
}

/**
 * Elect and connect to backend
 */
func (this *Server) electAndConnectVxlan(pool *connPool, hash uint64) (net.Conn, *core.Backend, error) {
	backend, err := this.scheduler.TakeBackend(core.VXlanContext{
		Hash: hash,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("Could not elect backend for clientAddr %d: %v", hash, err)
	}

	host := backend.Host
	port := backend.Port

	addrStr := host + ":" + port
	//fmt.Println("Hash is sent to: ", hash)
	//fmt.Println("Elected backend: ", host+":"+port)
	conn := pool.get(addrStr)
	if conn != nil {
		return conn, backend, nil
	}

	addr, err := net.ResolveUDPAddr("udp", addrStr)
	if err != nil {
		fmt.Println("Resolv addrStr", addrStr)
		return nil, nil, fmt.Errorf("Could not resolve udp address %s: %v", addrStr, err)
	}

	if this.cfg.Vxlan.Transparent {
		conn, err = udpfacade.DialUDPFrom(nil, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not dial UDP addr %v from %v: %v", addr, hash, err)
		}
	}
	if this.cfg.Vxlan.Source != "" {
		laddrStr := this.cfg.Vxlan.Source + ":0" // 0 means "pick a random source port"
		laddr, err := net.ResolveUDPAddr("udp", laddrStr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not resolve udp local address %s: %v", laddrStr, err)
		}
		conn, err = net.DialUDP("udp", laddr, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not dial UDP addr %v from %v: %v", addr, laddr, err)
		}
	} else {
		conn, err = net.DialUDP("udp", nil, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not dial UDP addr %v: %v", addr, err)
		}
	}

	pool.put(addrStr, conn)

	return conn, backend, nil
}

/**
 * Elect and connect to backend
 */
func (this *Server) electAndConnectGeneve(pool *connPool, hash uint64) (net.Conn, *core.Backend, error) {
	backend, err := this.scheduler.TakeBackend(core.VXlanContext{
		Hash: hash,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("Could not elect backend for clientAddr %d: %v", hash, err)
	}

	host := backend.Host
	port := backend.Port

	addrStr := host + ":" + port
	//fmt.Println("Hash is sent to: ", hash)
	//fmt.Println("Elected backend: ", host+":"+port)
	conn := pool.get(addrStr)
	if conn != nil {
		return conn, backend, nil
	}

	addr, err := net.ResolveUDPAddr("udp", addrStr)
	if err != nil {
		return nil, nil, fmt.Errorf("Could not resolve udp address %s: %v", addrStr, err)
	}

	if this.cfg.Geneve.Transparent {
		conn, err = udpfacade.DialUDPFrom(nil, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not dial UDP addr %v from %v: %v", addr, hash, err)
		}
	}
	if this.cfg.Geneve.Source != "" {
		laddrStr := this.cfg.Geneve.Source + ":0" // 0 means "pick a random source port"
		laddr, err := net.ResolveUDPAddr("udp", laddrStr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not resolve udp local address %s: %v", laddrStr, err)
		}
		conn, err = net.DialUDP("udp", laddr, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not dial UDP addr %v from %v: %v", addr, laddr, err)
		}
	} else {
		conn, err = net.DialUDP("udp", nil, addr)
		if err != nil {
			return nil, nil, fmt.Errorf("Could not dial UDP addr %v: %v", addr, err)
		}
	}

	pool.put(addrStr, conn)

	return conn, backend, nil
}

/**
 * Get or create session
 */
func (this *Server) getOrCreateSession(cfg session.Config, clientAddr *net.UDPAddr) (*session.Session, error) {
	//key := clientAddr.String()
	key := this.IPtoU64(clientAddr.IP)
	this.mu.Lock()
	defer this.mu.Unlock()

	s, ok := this.sessions[key]

	// session exists and is not done yet
	if ok && !s.IsDone() {
		return s, nil
	}

	// session exists but should be replaced with a new one
	if ok {
		go func() { s.Close() }()
	}

	conn, backend, err := this.electAndConnect(nil, clientAddr)
	if err != nil {
		return nil, fmt.Errorf("Could not elect/connect to backend: %v", err)
	}

	s = session.NewSession(clientAddr, conn, *backend, this.scheduler, cfg)
	if !cfg.Transparent {
		s.ListenResponses(this.serverConn)
	}

	this.sessions[key] = s

	this.scheduler.StatsHandler.Connections <- uint(len(this.sessions))

	return s, nil
}

/**
 * Get or create session
 */
func (this *Server) getOrCreateSessionVxlan(cfg session.Config, vxlanHdr VXLANHdr) (*session.Session, error) {
	//key := this.makeHashVxlan(vxlanHdr)
	key := this.makeu64HashVxlan(vxlanHdr)
	//fmt.Println("getOrCreateSessionVxlan Session Hash: ", key)
	this.mu.Lock()
	defer this.mu.Unlock()

	s, ok := this.sessions[key]

	// session exists and is not done yet
	if ok && !s.IsDone() {
		//fmt.Println("getOrCreateSessionVxlan found session ", s)
		return s, nil
	}

	// session exists but should be replaced with a new one
	if ok {
		go func() { s.Close() }()
	}

	conn, backend, err := this.electAndConnectVxlan(nil, key)
	if err != nil {
		return nil, fmt.Errorf("Could not elect/connect to backend: %v", err)
	}
	//println("Before NewSessionVXlan: ", conn.RemoteAddr().String())
	s = session.NewSessionVXlan(key, conn, *backend, this.scheduler, cfg)
	//VXLAN sends only, there are no responeses
	//if !cfg.Transparent {
	//	s.ListenResponses(this.serverConn)
	//}

	this.sessions[key] = s
	this.scheduler.StatsHandler.Connections <- uint(len(this.sessions))

	return s, nil
}

func (this *Server) getOrCreateSessionGeneve(cfg session.Config, vxlanHdr VXLANHdr) (*session.Session, error) {
	//key := this.makeHashVxlan(vxlanHdr)
	key := this.makeu64HashVxlan(vxlanHdr)
	//fmt.Println("getOrCreateSessionVxlan Session Hash: ", key)
	this.mu.Lock()
	defer this.mu.Unlock()

	s, ok := this.sessions[key]

	// session exists and is not done yet
	if ok && !s.IsDone() {
		//fmt.Println("getOrCreateSessionVxlan found session ", s)
		return s, nil
	}

	// session exists but should be replaced with a new one
	if ok {
		go func() { s.Close() }()
	}

	conn, backend, err := this.electAndConnectGeneve(nil, key)
	if err != nil {
		return nil, fmt.Errorf("Could not elect/connect to backend: %v", err)
	}
	//println("Before NewSessionVXlan: ", conn.RemoteAddr().String())
	s = session.NewSessionVXlan(key, conn, *backend, this.scheduler, cfg)
	//VXLAN sends only, there are no responeses
	//if !cfg.Transparent {
	//	s.ListenResponses(this.serverConn)
	//}

	this.sessions[key] = s
	this.scheduler.StatsHandler.Connections <- uint(len(this.sessions))

	return s, nil
}

/**
 * Get the session and send data via chosen session
 */
func (this *Server) proxy(cfg session.Config, clientAddr *net.UDPAddr, buf []byte) {
	s, err := this.getOrCreateSession(cfg, clientAddr)
	if err != nil {
		log.Error(err)
		return
	}

	err = s.Write(buf)
	if err != nil {
		log.Errorf("Could not write data to UDP 'session' %v: %v", s, err)
		return
	}
}

/**
 * Get the session and send data via chosen session for VXLAN
 */
func (this *Server) proxyVxlan(cfg session.Config, vxlanHdr VXLANHdr, buf []byte) {
	//fmt.Println("Server proxyVxlan()")
	s, err := this.getOrCreateSessionVxlan(cfg, vxlanHdr)
	if err != nil {
		log.Error(err)
		return
	}
	err = s.Write(buf)
	if err != nil {
		log.Errorf("Could not write data to UDP 'session' %v: %v", s, err)
		return
	}
}

/**
 * Get the session and send data via chosen session for VXLAN
 */
func (this *Server) proxyGeneve(cfg session.Config, vxlanHdr VXLANHdr, buf []byte) {
	//fmt.Println("Server proxyVxlan()")
	s, err := this.getOrCreateSessionGeneve(cfg, vxlanHdr)
	if err != nil {
		log.Error(err)
		return
	}
	//outgoing packet
	data := gopacket.NewSerializeBuffer()
	vxlan := &layers.VXLAN{
		ValidIDFlag: true,
		VNI:         1000,
	}
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	gopacket.SerializeLayers(data, opts, vxlan, gopacket.Payload(buf))
	buf = data.Bytes()
	err = s.Write(buf)
	if err != nil {
		log.Errorf("Could not write data to UDP 'session' %v: %v", s, err)
		return
	}
}

/**
 * Omit creating session, just send one packet of data
 */
func (this *Server) fireAndForget(pool *connPool, clientAddr *net.UDPAddr, buf []byte) error {
	conn, backend, err := this.electAndConnect(pool, clientAddr)
	if err != nil {
		return fmt.Errorf("Could not elect or connect to backend: %v", err)
	}

	n, err := conn.Write(buf)
	if err != nil {
		return fmt.Errorf("Could not write data to %v: %v", clientAddr, err)
	}

	if n != len(buf) {
		return fmt.Errorf("Failed to send full packet, expected size %d, actually sent %d", len(buf), n)
	}

	this.scheduler.IncrementTx(*backend, uint(n))

	return nil
}

/**
 * Stop, dropping all connections
 */
func (this *Server) Stop() {
	this.stop <- true
}

/**
 * Helper function to order ip
 * Return the IP address that has the higher value when each octet is compared
 * If equal returns the first
 */
func (this *Server) IPgtIP(ip1 net.IP, ip2 net.IP) net.IP {
	pip1 := net.ParseIP(ip1.String()).To4()
	pip2 := net.ParseIP(ip2.String()).To4()
	for i := 0; i < 4; i++ {
		if pip1[i] == pip2[i] {
			_ = "" //do nothing.
		} else if pip1[i] > pip2[i] {
			return ip1
		} else {
			return ip2
		}
	}
	return ip1
}

/**
 * Helper function to order ip
 * Return the IP address that has the lowest value when each octet is compared
 * If equal returns the first
 */
func (this *Server) IPltIP(ip1 net.IP, ip2 net.IP) net.IP {
	pip1 := net.ParseIP(ip1.String()).To4()
	pip2 := net.ParseIP(ip2.String()).To4()
	for i := 0; i < 4; i++ {
		if pip1[i] == pip2[i] {
			_ = "" //do nothing.
		} else if pip1[i] < pip2[i] {
			return ip1
		} else {
			return ip2
		}
	}
	return ip1
}

/**
 * Helper function to make hash string
 * takes a vxlan header sorts the IP and ports and makes the hash from the ordered strings (high to low)
 */
func (this *Server) makeHashVxlan(vxlanHdr VXLANHdr) string {
	high := this.IPgtIP(vxlanHdr.Ip.SrcIP, vxlanHdr.Ip.DstIP).String()
	low := this.IPltIP(vxlanHdr.Ip.SrcIP, vxlanHdr.Ip.DstIP).String()
	var highPort (string) = "0"
	var lowPort (string) = "0"
	if vxlanHdr.Tcp.SrcPort > vxlanHdr.Tcp.DstPort {
		highPort = string(strconv.FormatUint(uint64(vxlanHdr.Tcp.SrcPort), 10))
		lowPort = string(strconv.FormatUint(uint64(vxlanHdr.Tcp.DstPort), 10))
	} else {
		lowPort = string(strconv.FormatUint(uint64(vxlanHdr.Tcp.SrcPort), 10))
		highPort = string(strconv.FormatUint(uint64(vxlanHdr.Tcp.DstPort), 10))
	}
	return high + ":" + highPort + "-" + low + ":" + lowPort
}

/**
 * Helper function to make hash of uint64
 * takes a vxlan header sorts the IP and ports and makes the hash from the ordered strings (high to low)
 */
func (this *Server) makeu64HashVxlan(vxlanHdr VXLANHdr) uint64 {
	var highPort (uint64) = 0
	var lowPort (uint64) = 0
	var highIP (uint64) = 0
	var lowIP (uint64) = 0
	var Return (uint64)
	ip1 := this.IPtoU64(vxlanHdr.Ip.SrcIP)
	ip2 := this.IPtoU64(vxlanHdr.Ip.DstIP)
	if ip1 > ip2 {
		highIP = ip1
		lowIP = ip2
	} else {
		highIP = ip2
		lowIP = ip1
	}
	if vxlanHdr.Tcp.SrcPort > vxlanHdr.Tcp.DstPort {
		highPort = uint64(vxlanHdr.Tcp.SrcPort)
		lowPort = uint64(vxlanHdr.Tcp.DstPort)
	} else {
		lowPort = uint64(vxlanHdr.Tcp.SrcPort)
		highPort = uint64(vxlanHdr.Tcp.DstPort)
	}
	//fmt.Print("Tcp.SrcPort", uint64(vxlanHdr.Tcp.SrcPort))
	//fmt.Print("Tcp.DstPort", uint64(vxlanHdr.Tcp.DstPort))

	//fmt.Print(" highIP ", highIP)
	//fmt.Print(" lowIP ", lowIP)
	//fmt.Print(" highPort ", highPort)
	//fmt.Print(" lowPort ", lowPort)
	//max value is: 18446744073709551615
	Return = highIP + 100000000000000 + lowIP*10000000000 + highPort*10000 + lowPort
	//fmt.Println("Hash: ", Return)
	return Return
}

// Takes an IP for example 10.10.10.10
// and returns the two last most significant part of the IP
// (256+10)+10
func (this *Server) IPtoU64(ip1 net.IP) uint64 {
	pip1 := net.ParseIP(ip1.String()).To4()
	var result uint64
	var result3 uint64
	var result4 uint64
	result = 0
	result3 = 0
	result4 = 0
	for i := 0; i < 4; i++ {
		if i == 2 {
			result3 = uint64(pip1[i])
		}
		if i == 3 {
			result4 = uint64(pip1[i]) * 100000
		}
	}
	result = result3 + result4
	//fmt.Println(" IP: ", result)
	return result
}

func decodeGeneveOption(data []byte) (*GeneveOption, uint8, error) {
	opt := &GeneveOption{}

	opt.Class = binary.BigEndian.Uint16(data[0:2])
	opt.Type = data[2]
	opt.Flags = data[3] >> 4
	opt.Length = (data[3]&0xf)*4 + 4
	opt.Data = make([]byte, opt.Length-4)
	copy(opt.Data, data[4:opt.Length])
	return opt, opt.Length, nil
}
