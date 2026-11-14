package nex

// 本体どうしの通信の中継（38_サーバー機能の検討.md の方法 A）。YW2_RELAY=true のときだけ動く。
// 相手へ教える URL を中継のポートに書き換えて、対戦のパケットをここへ通す。
//
// インターネット越しでは、本体の URL のアドレスと実際の送信元が違う（NAT）。
// 最初に届いたパケットで本当の送信元を覚え、以後はそこへ返す。
// ポートは相手を探し始めたとき（ReplaceURL）にもらい、対戦が終わってしばらくすると返す。
// 足りないときや相手探しの通信がないときは書き換えず、本体どうしの直接の通信に任せる（パーティは取れない）。
// 別の回線どうしで動くかは、まだ本物の対戦で確かめていない。

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	nattraversal "github.com/PretendoNetwork/nex-protocols-go/v2/nat-traversal"
	secure "github.com/PretendoNetwork/nex-protocols-go/v2/secure-connection"
)

const (
	relayFirstPort = 60100
	relayPortCount = 100
	relayIdleLimit = 60 * time.Minute // * 相手を探している人。やめたことは分からないので、長く待って返す
	relayDoneLimit = 3 * time.Minute  // * 対戦の通信があった人。対戦中は止まらないので、止まれば終わったとみなす
)

type relayStation struct {
	pid        uint32
	port       int
	socket     *net.UDPConn
	candidates []*net.UDPAddr // * 本体がいそうなアドレス。seen が分かるまで全部へ送る
	seen       *net.UDPAddr   // * 実際に届いたときの送信元
	partner    uint32         // * 対戦の相手
	sawPackets bool           // * このポートで対戦の通信があった
	blocked    bool           // * イベントの違反などで止めた。次の部屋に入るまで通さない
	lastActive time.Time
}

type packetRelay struct {
	mutex     sync.Mutex
	publicIP  string
	byPID     map[uint32]*relayStation
	freePorts []int
	idleLimit time.Duration
	doneLimit time.Duration
	connected func(pid uint32) bool // * サーバーとまだつながっているか
	dumpFile  *os.File
	dumpMutex sync.Mutex
}

var activeRelay *packetRelay

func relayEnabled() bool {
	return os.Getenv("YW2_RELAY") == "true"
}

func relayPublicHost() string {
	if host := os.Getenv("YW2_RELAY_HOST"); host != "" {
		return host
	}
	return os.Getenv("PN_YKW2_SECURE_SERVER_HOST")
}

func newPacketRelay(publicHost string, firstPort int, portCount int) *packetRelay {
	relay := &packetRelay{
		publicIP:  publicHost,
		byPID:     make(map[uint32]*relayStation),
		idleLimit: relayIdleLimit,
		doneLimit: relayDoneLimit,
	}
	for port := firstPort; port < firstPort+portCount; port++ {
		relay.freePorts = append(relay.freePorts, port)
	}

	// * 通信の丸ごと保存は、調査のときだけ。ふだんは残さない
	if os.Getenv("YW2_RELAY_DUMP") == "true" {
		name := fmt.Sprintf("/relay-dump/relay-%s.txt", time.Now().UTC().Format("20060102-150405"))
		file, err := os.Create(name)
		if err != nil {
			fmt.Printf("[YW2-RELAY] cannot create dump file: %v\n", err)
		} else {
			relay.dumpFile = file
			fmt.Printf("[YW2-RELAY] dump file: %s\n", name)
		}
	}

	return relay
}

func (relay *packetRelay) dump(from string, to string, viaPort int, data []byte) {
	if relay.dumpFile == nil {
		return
	}

	relay.dumpMutex.Lock()
	defer relay.dumpMutex.Unlock()
	fmt.Fprintf(relay.dumpFile, "%s %s %s %d %d %x\n", time.Now().UTC().Format("15:04:05.000"), from, to, viaPort, len(data), data)
}

// * pid の中継ポートを用意する。ポートが足りなければ nil を返す
func (relay *packetRelay) assign(pid uint32, candidates []*net.UDPAddr) (*relayStation, error) {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	if station, ok := relay.byPID[pid]; ok {
		station.candidates = candidates
		station.seen = nil
		station.sawPackets = false
		station.lastActive = time.Now()
		return station, nil
	}

	for len(relay.freePorts) > 0 {
		port := relay.freePorts[0]
		relay.freePorts = relay.freePorts[1:]

		socket, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err != nil {
			// * このポートは使わず、次を試す
			fmt.Printf("[YW2-RELAY] cannot use port %d: %v\n", port, err)
			continue
		}

		station := &relayStation{pid: pid, port: port, socket: socket, candidates: candidates, lastActive: time.Now()}
		relay.byPID[pid] = station
		go relay.receive(station)
		return station, nil
	}

	relayExhausted.Add(1)
	fmt.Printf("[YW2-RELAY] no relay ports left for pid %d\n", pid)
	return nil, fmt.Errorf("no relay ports left")
}

// * しばらく使っていないポートを返す
func (relay *packetRelay) releaseIdle(now time.Time) {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	for pid, station := range relay.byPID {
		limit := relay.idleLimit
		if station.sawPackets {
			limit = relay.doneLimit
		} else if relay.connected != nil && !relay.connected(pid) {
			// * 相手を探している途中でサーバーから切れた
			limit = 0
		}
		if now.Sub(station.lastActive) < limit {
			continue
		}
		delete(relay.byPID, pid)
		station.socket.Close()
		relay.freePorts = append(relay.freePorts, station.port)
		fmt.Printf("[YW2-RELAY] pid %d released port %d\n", pid, station.port)
	}
}

func (relay *packetRelay) setPartners(one uint32, two uint32) {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	if station, ok := relay.byPID[one]; ok {
		station.partner = two
		station.lastActive = time.Now()
	}
	if station, ok := relay.byPID[two]; ok {
		station.partner = one
		station.lastActive = time.Now()
	}
}

// * その人と、対戦の相手の通信を止める
func (relay *packetRelay) block(pid uint32) {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	station, ok := relay.byPID[pid]
	if !ok {
		return
	}

	station.blocked = true
	if partner, ok := relay.byPID[station.partner]; ok {
		partner.blocked = true
	}
}

func (relay *packetRelay) unblock(pid uint32) {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	if station, ok := relay.byPID[pid]; ok {
		station.blocked = false
	}
}

func (relay *packetRelay) isBlocked(one uint32, two uint32) bool {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	for _, pid := range []uint32{one, two} {
		if station, ok := relay.byPID[pid]; ok && station.blocked {
			return true
		}
	}

	return false
}

func (relay *packetRelay) candidatesOf(pid uint32) []*net.UDPAddr {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	if station, ok := relay.byPID[pid]; ok {
		return append([]*net.UDPAddr(nil), station.candidates...)
	}
	return nil
}

// * そのポートを、中継に使わないようにする
func (relay *packetRelay) reservePort(port int) {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	for n, free := range relay.freePorts {
		if free == port {
			relay.freePorts = append(relay.freePorts[:n], relay.freePorts[n+1:]...)
			return
		}
	}
}

func sameAddress(one *net.UDPAddr, two *net.UDPAddr) bool {
	return one != nil && two != nil && one.Port == two.Port && one.IP.Equal(two.IP)
}

func (station *relayStation) hasIP(ip net.IP) bool {
	for _, candidate := range station.candidates {
		if candidate.IP.Equal(ip) {
			return true
		}
	}
	return station.seen != nil && station.seen.IP.Equal(ip)
}

// * 送信元のアドレスから、どの本体の送信かを探す。分からなければ nil（捨てる）
func (relay *packetRelay) sender(source *net.UDPAddr, destination *relayStation) *relayStation {
	var match *relayStation
	matches := 0

	for _, station := range relay.byPID {
		if station == destination {
			continue
		}
		if sameAddress(station.seen, source) {
			return station
		}
		for _, candidate := range station.candidates {
			if sameAddress(candidate, source) {
				return station
			}
		}
		if station.hasIP(source.IP) {
			match = station
			matches++
		}
	}

	// * NAT でポートが変わっている。相手が分かっていて同じ IP なら、その人と判断する
	if partner, ok := relay.byPID[destination.partner]; ok && partner.hasIP(source.IP) {
		return partner
	}

	// * 同じ家から何人もいるときは、決めつけない
	if matches == 1 {
		return match
	}
	return nil
}

func (relay *packetRelay) receive(destination *relayStation) {
	buffer := make([]byte, 2048)

	for {
		size, source, err := destination.socket.ReadFromUDP(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				fmt.Printf("[YW2-RELAY] read failed on %d: %v\n", destination.port, err)
			}
			return
		}

		relay.mutex.Lock()
		sender := relay.sender(source, destination)
		if destination.blocked || (sender != nil && sender.blocked) {
			var blockedPID uint32
			if sender != nil {
				blockedPID = sender.pid
			}
			relay.mutex.Unlock()

			// * 相手には届けないが、止めたあとに届いたパーティも記録には残す
			if blockedPID != 0 && history != nil {
				safely("blocked packet", func() { history.onBlockedPacket(blockedPID, buffer[:size]) })
			}
			continue
		}
		var senderPID uint32
		var senderSocket *net.UDPConn
		if sender != nil {
			if !sameAddress(sender.seen, source) {
				sender.seen = source
				fmt.Printf("[YW2-RELAY] pid %d sends from %s\n", sender.pid, source)
			}
			now := time.Now()
			sender.lastActive = now
			destination.lastActive = now
			sender.sawPackets = true
			destination.sawPackets = true
			if sender.partner == 0 {
				sender.partner = destination.pid
			}
			if destination.partner == 0 {
				destination.partner = sender.pid
			}
			senderPID = sender.pid
			senderSocket = sender.socket
		}
		var targets []*net.UDPAddr
		if destination.seen != nil {
			targets = []*net.UDPAddr{destination.seen}
		} else {
			targets = append(targets, destination.candidates...)
		}
		destinationPID := destination.pid
		destinationPort := destination.port
		relay.mutex.Unlock()

		data := buffer[:size]

		if senderSocket == nil {
			relay.dump(source.String(), "unknown-sender", destinationPort, data)
			continue
		}

		relay.dump(strconv.FormatUint(uint64(senderPID), 10), strconv.FormatUint(uint64(destinationPID), 10), destinationPort, data)

		if history != nil {
			// * 読み取りで想定外のことが起きたパケットは、相手にも届けない
			if !safely("relay packet", func() {
				history.fakeHealForTest(senderPID, data)
				history.rewriteEventPacket(senderPID, data)
				history.watchEventResult(senderPID, data)
				history.onPacket(senderPID, data)
			}) {
				continue
			}

			// * このパケットで違反が分かったときは、相手に届けない
			if relay.isBlocked(senderPID, destinationPID) {
				continue
			}
		}

		// * 相手には、送信元の中継ポートから届いたように見せる
		for _, target := range targets {
			if _, err := senderSocket.WriteToUDP(data, target); err != nil {
				fmt.Printf("[YW2-RELAY] write failed to %s: %v\n", target, err)
			}
		}
	}
}

// * 本体のいそうなアドレスを集める。URL のアドレスと、サーバーから見えた送信元の IP
func relayCandidates(connection *nex.PRUDPConnection, port int, urlAddress string) []*net.UDPAddr {
	candidates := []*net.UDPAddr{}
	add := func(ip net.IP) {
		if ip == nil {
			return
		}
		for _, candidate := range candidates {
			if candidate.IP.Equal(ip) {
				return
			}
		}
		candidates = append(candidates, &net.UDPAddr{IP: ip, Port: port})
	}

	add(net.ParseIP(urlAddress))
	if address, ok := connection.Address().(*net.UDPAddr); ok {
		add(address.IP)
	}
	return candidates
}

func (relay *packetRelay) applyPort(connection *nex.PRUDPConnection, station *relayStation) {
	for i := range connection.StationURLs {
		url := &connection.StationURLs[i]
		url.SetAddress(relay.publicIP)
		url.SetPortNumber(uint16(station.port))
		fmt.Printf("[YW2-RELAY] pid %d now %s\n", station.pid, url.URL())
	}
}

// * 本体が URL を差し替えたときは、中継先だけを新しいアドレスにする。相手に見せる URL は中継のまま
func (relay *packetRelay) replaced(connection *nex.PRUDPConnection, target types.StationURL, url types.StationURL) {
	pid := uint32(connection.PID())
	fmt.Printf("[YW2-RELAY] pid %d replace %s -> %s\n", pid, target.URL(), url.URL())

	if url.IsPublic() {
		return
	}

	address, addressOk := url.Address()
	port, portOk := url.PortNumber()
	if !addressOk || !portOk {
		return
	}

	if endpoint, ok := connection.Endpoint().(*nex.PRUDPEndPoint); ok && relay.connected == nil {
		relay.connected = func(pid uint32) bool { return endpoint.FindConnectionByPID(uint64(pid)) != nil }
	}

	// * ここでポートをもらう。相手を探し始めるたびに通る
	station, err := relay.assign(pid, relayCandidates(connection, int(port), address))
	if err != nil {
		fmt.Printf("[YW2-RELAY] pid %d: %v\n", pid, err)
		return
	}
	relay.applyPort(connection, station)
}

// * 相手に「ここへ確認を送って」と伝える URL も、中継ポートに書き換える。相手の組み合わせもここで分かる
func (relay *packetRelay) probeURL(connection *nex.PRUDPConnection, targetList types.List[types.String], stationToProbe types.String) types.String {
	pid := uint32(connection.PID())

	relay.mutex.Lock()
	station, ok := relay.byPID[pid]
	relay.mutex.Unlock()

	if !ok {
		fmt.Printf("[YW2-RELAY] pid %d probe not relayed: %s\n", pid, string(stationToProbe))
		return stationToProbe
	}

	endpoint, isEndpoint := connection.Endpoint().(*nex.PRUDPEndPoint)
	for _, target := range targetList {
		targetURL := types.NewStationURL(target)
		if connectionID, hasID := targetURL.RVConnectionID(); hasID && isEndpoint {
			if other := endpoint.FindConnectionByID(connectionID); other != nil {
				relay.setPartners(pid, uint32(other.PID()))
			}
		}
	}

	url := types.NewStationURL(stationToProbe)
	url.SetAddress(relay.publicIP)
	url.SetPortNumber(uint16(station.port))
	fmt.Printf("[YW2-RELAY] pid %d probe %s -> %s\n", pid, string(stationToProbe), url.URL())

	return types.NewString(url.URL())
}

func registerPacketRelay(secureProtocol *secure.Protocol, natTraversalProtocol *nattraversal.Protocol) {
	if !relayEnabled() {
		return
	}

	relay := newPacketRelay(relayPublicHost(), relayFirstPort, relayPortCount)
	activeRelay = relay
	startVirtualHost(relay)
	originalReplaceURL := secureProtocol.ReplaceURL
	originalProbe := natTraversalProtocol.RequestProbeInitiationExt

	secureProtocol.SetHandlerReplaceURL(func(err error, packet nex.PacketInterface, callID uint32, target types.StationURL, url types.StationURL) (*nex.RMCMessage, *nex.Error) {
		if err == nil {
			relay.replaced(packet.Sender().(*nex.PRUDPConnection), target, url)
		}

		return originalReplaceURL(err, packet, callID, target, url)
	})

	natTraversalProtocol.SetHandlerRequestProbeInitiationExt(func(err error, packet nex.PacketInterface, callID uint32, targetList types.List[types.String], stationToProbe types.String) (*nex.RMCMessage, *nex.Error) {
		if err == nil {
			connection := packet.Sender().(*nex.PRUDPConnection)
			// * 試験用: サーバーが持ち主を演じる部屋では、相手へ直接、確認を送る
			targets := make([]string, 0, len(targetList))
			for _, target := range targetList {
				targets = append(targets, string(target))
			}
			if host := vhostOfProbe(uint32(connection.PID()), targets); host != nil {
				host.startProbing(relay.candidatesOf(uint32(connection.PID())))
			}
			stationToProbe = relay.probeURL(connection, targetList, stationToProbe)
		}

		return originalProbe(err, packet, callID, targetList, stationToProbe)
	})

	go func() {
		relay.saveStatus()
		for now := range time.Tick(time.Minute) {
			relay.releaseIdle(now)
			relay.saveStatus()
		}
	}()

	fmt.Printf("[YW2-RELAY] enabled, host %s, %d ports\n", relay.publicIP, relayPortCount)
}
