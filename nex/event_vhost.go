package nex

// 試験用: サーバーが部屋の持ち主を演じる（44_対戦の接続手順の解読.md）。
// YW2_DEV_VHOST_PID の 2 人を、それぞれ別の部屋の参加者にして、中身をつなぐ（event_bridge.go）。
// 本番では使わない。

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	match_making "github.com/PretendoNetwork/nex-protocols-go/v2/match-making"
	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

const (
	vhostPort      = 60198 // * 2 人分で 60198 と 60199
	vhostConstID   = 100   // * 持ち主の RVCID。2 人目は 101。本物の接続と重ならない値
	vhostStream    = 0x44
	vhostSender    = 1 // * 持ち主はステーション 0、ゲームの通信の送信元の番号は 1
	vhostOfferWait = 10 * time.Second

	// * 接続後の宛先は、相手の RVCID ではなくステーション番号のビット。参加者は番号 1 なので 2。
	// * RVCID を入れると、値の 2 のビットが立っていない本体（1、4、5、8、9、12...）だけ受け取らず、止まっていた
	vhostDestPeer = 2

	piaHeaderSize     = 12
	piaMessageHeader  = 20
	piaProtoNAT       = 0x0400
	piaProtoStation   = 0x0100
	piaProtoMesh      = 0x0200
	piaProtoClock     = 0x0210
	piaProtoKeepAlive = 0x0600
	piaProtoGame      = 0x2000
	piaFlagProbe      = 0x08

	piaKindAck     = 0xF003
	piaKindStart   = 0xF00A
	piaKindHello   = 0xF00B
	piaKindPlayer  = 0xF009
	piaKindJoined  = 0xF002
	piaKindReady   = 0x1001
	piaKindParty   = 0x1006
	piaKindSeed    = 0x1012
	piaKindBattle  = 0x1004
	piaKindOrder   = 0x1005
	piaKindRequest = 0x1007
	piaKindAction  = 0x1002
	piaKindTurn    = 0x1003
	piaKindState   = 0x1008
	piaKindRematch = 0x1010
	piaKindDecline = 0x1011
)

type piaMessage struct {
	Flags   byte
	Station byte
	Dest    uint32
	Src     uint32
	Proto   uint16
	Port    uint16
	Body    []byte
}

func parsePiaPacket(raw []byte) (conn byte, packetID uint16, srcTimer uint16, messages []piaMessage, ok bool) {
	if len(raw) < piaHeaderSize+piaSignatureSize || raw[0] != 0x32 || raw[1] != 0xAB || raw[2] != 0x98 || raw[3] != 0x64 {
		return 0, 0, 0, nil, false
	}

	conn = raw[5]
	packetID = binary.BigEndian.Uint16(raw[6:])
	srcTimer = binary.BigEndian.Uint16(raw[8:])

	end := len(raw) - piaSignatureSize
	for at := piaHeaderSize; at+piaMessageHeader <= end; {
		size := int(binary.BigEndian.Uint16(raw[at+2:]))
		if at+piaMessageHeader+size > end {
			return conn, packetID, srcTimer, messages, false
		}
		messages = append(messages, piaMessage{
			Flags:   raw[at],
			Station: raw[at+1],
			Dest:    binary.BigEndian.Uint32(raw[at+4:]),
			Src:     binary.BigEndian.Uint32(raw[at+8:]),
			Proto:   binary.BigEndian.Uint16(raw[at+12:]),
			Port:    binary.BigEndian.Uint16(raw[at+14:]),
			Body:    raw[at+piaMessageHeader : at+piaMessageHeader+size],
		})
		at = (at + piaMessageHeader + size + 3) &^ 3
	}

	return conn, packetID, srcTimer, messages, true
}

func buildPiaPacket(key []byte, conn byte, packetID uint16, srcTimer uint16, dstTimer uint16, messages ...piaMessage) []byte {
	packet := []byte{0x32, 0xAB, 0x98, 0x64, 0x01, conn}
	packet = binary.BigEndian.AppendUint16(packet, packetID)
	packet = binary.BigEndian.AppendUint16(packet, srcTimer)
	packet = binary.BigEndian.AppendUint16(packet, dstTimer)

	for _, message := range messages {
		for len(packet)%4 != 0 {
			packet = append(packet, 0)
		}
		packet = append(packet, message.Flags, message.Station)
		packet = binary.BigEndian.AppendUint16(packet, uint16(len(message.Body)))
		packet = binary.BigEndian.AppendUint32(packet, message.Dest)
		packet = binary.BigEndian.AppendUint32(packet, message.Src)
		packet = binary.BigEndian.AppendUint16(packet, message.Proto)
		packet = binary.BigEndian.AppendUint16(packet, message.Port)
		packet = append(packet, 0, 0, 0, 0)
		packet = append(packet, message.Body...)
	}

	return append(packet, piaSignature(packet, key)...)
}

// * ゲームの通信を 1 つ作る。先頭と末尾に区切りが入る
func buildPiaGame(seq uint16, ack uint32, kind uint16, data []byte) []byte {
	body := append([]byte(nil), piaMessageMark...)
	body = binary.LittleEndian.AppendUint16(body, seq)
	body = append(body, 0x00, vhostStream)
	body = binary.LittleEndian.AppendUint32(body, ack)
	body = binary.LittleEndian.AppendUint16(body, vhostSender)
	body = binary.LittleEndian.AppendUint16(body, kind)
	body = append(body, byte(seq)^byte(kind)^vhostSender, byte(kind>>8), 0x00, vhostStream)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(data)))
	body = append(body, data...)
	return append(body, piaMessageMark...)
}

type piaGame struct {
	Seq  uint16
	Ack  uint32
	Kind uint16
	Data []byte
}

func parsePiaGame(body []byte) (piaGame, bool) {
	if len(body) < 28 || !bytes.Equal(body[:4], piaMessageMark) {
		return piaGame{}, false
	}
	size := binary.LittleEndian.Uint32(body[20:])
	if int(size) > len(body)-24 {
		return piaGame{}, false
	}
	return piaGame{
		Seq:  binary.LittleEndian.Uint16(body[4:]),
		Ack:  binary.LittleEndian.Uint32(body[8:]),
		Kind: binary.LittleEndian.Uint16(body[14:]),
		Data: body[24 : 24+size],
	}, true
}

type vhostPending struct {
	conn   byte
	nonce  uint32
	proto  uint16
	body   []byte
	sentAt time.Time
}

type vhostSent struct {
	seq    uint16
	kind   uint16
	data   []byte
	sentAt time.Time
}

type virtualHost struct {
	mutex    *sync.Mutex // * つなぐ 2 人で 1 つ
	socket   *net.UDPConn
	bridge   *vhostBridge
	side     int
	constID  uint32
	key      []byte
	bot      uint32
	gid      uint32
	publicIP net.IP
	port     int
	start    time.Time

	targets   []*net.UDPAddr // * 相手がいそうなアドレス。応答が来るまで確認を送り続ける
	probeAt   time.Time
	probeEnd  time.Time
	peer      *net.UDPAddr
	peerConst uint32
	peerConn  byte
	peerTimer uint16
	peerAt    time.Time
	blocks    []byte // * 相手のアドレスの部分
	connID    byte   // * 接続ごとに変える。本物も毎回違う値で、同じ値を続けると本体が受け取らないことがある
	joined    bool
	peerReady bool // * 相手が部屋に入ったあとの通信を送ってきた
	probed    bool
	nonce     uint32
	packetID  uint16
	pending   []*vhostPending

	seq         uint16
	received    uint32
	lastPeerSeq uint16
	sent        []*vhostSent
	ackTick     uint32
	meshCounter uint32
	meshStarted bool
	meshAcked   bool
	meshDone    bool      // * 最初の部屋の情報に返事が来た。それまでは更新の送り直ししかしない
	meshSince   time.Time // * 最初の部屋の情報を送った時刻
	stuckLog    bool
	silent      bool // * 試験: 持ち主が落ちたことにして、何も送らない（持ち主の引き継ぎを見る）

	lastAck   time.Time
	lastPing  time.Time
	lastMesh  time.Time
	seenKinds map[uint16]bool
}

func newVirtualHost(publicIP string, port int, side int, bridge *vhostBridge) (*virtualHost, error) {
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}
	host := &virtualHost{mutex: &bridge.mutex, socket: socket, bridge: bridge, side: side, constID: vhostConstID + uint32(side), publicIP: net.ParseIP(publicIP).To4(), port: port, start: time.Now()}
	bridge.sides[side] = host
	host.resetLocked()
	go host.receive()
	go host.tick()
	return host, nil
}

func (host *virtualHost) resetLocked() {
	host.targets = nil
	host.probeEnd = time.Time{}
	host.peer = nil
	host.joined = false
	host.peerReady = false
	host.probed = false
	host.blocks = nil
	host.pending = nil
	host.packetID = 0
	host.seq = 0
	host.received = 0
	host.lastPeerSeq = 0
	host.sent = nil
	host.ackTick = 0x0C
	host.meshCounter = 0xFFFFF82F
	host.meshStarted = false
	host.stuckLog = false
	host.silent = false
	host.meshDone = false
	host.meshAcked = false
	host.seenKinds = make(map[uint16]bool)

	random := make([]byte, 5)
	rand.Read(random)
	host.nonce = binary.BigEndian.Uint32(random)
	host.connID = random[4]
	if host.connID < 2 {
		host.connID = 2
	}
}

// * 持ち主のアドレスの部分。本物は 2 組で、どちらもこのサーバーにする
func (host *virtualHost) addressBlocks() []byte {
	block := func() []byte {
		out := append([]byte(nil), host.publicIP...)
		out = binary.BigEndian.AppendUint16(out, uint16(host.port))
		out = append(out, 0, 0)
		out = binary.BigEndian.AppendUint32(out, host.bot)
		out = binary.BigEndian.AppendUint64(out, uint64(host.constID))
		return append(out, 0x01, 0x0F, 0x00, 0x01, 0x02)
	}
	out := block()
	out = append(out, 0x03, 0x00)
	out = append(out, block()...)
	return append(out, 0x00, 0x00)
}

func (host *virtualHost) millis() uint64 {
	return uint64(time.Since(host.start).Milliseconds())
}

func (host *virtualHost) sendLocked(conn byte, messages ...piaMessage) {
	if host.peer == nil || host.silent {
		return
	}

	id := uint16(0)
	if conn != 0 {
		host.packetID++
		id = host.packetID
	}
	dst := uint16(0)
	if !host.peerAt.IsZero() {
		dst = host.peerTimer + uint16(time.Since(host.peerAt).Milliseconds())
	}
	packet := buildPiaPacket(host.key, conn, id, uint16(host.millis()), dst, messages...)
	if _, err := host.socket.WriteToUDP(packet, host.peer); err != nil {
		fmt.Printf("[YW2-VHOST] send failed: %v\n", err)
	}
}

func (host *virtualHost) unconnected(proto uint16, port uint16, flags byte, body []byte) piaMessage {
	return piaMessage{Flags: flags, Src: host.constID, Proto: proto, Port: port, Body: body}
}

func (host *virtualHost) connected(proto uint16, port uint16, dest uint32, body []byte) piaMessage {
	return piaMessage{Dest: dest, Src: host.constID, Proto: proto, Port: port, Body: body}
}

func (host *virtualHost) sendPendingLocked(conn byte, proto uint16, port uint16, dest uint32, body []byte) {
	host.nonce++
	nonce := host.nonce
	full := binary.BigEndian.AppendUint32(append([]byte(nil), body...), nonce)
	host.pending = append(host.pending, &vhostPending{conn: conn, nonce: nonce, proto: proto, body: full, sentAt: time.Now()})
	host.sendLocked(conn, piaMessage{Dest: dest, Src: host.constID, Proto: proto, Port: port, Body: full})
}

func (host *virtualHost) ackStationLocked(nonce []byte) {
	body := append([]byte{0x05, 0, 0, 0}, nonce...)
	host.sendLocked(0, host.unconnected(piaProtoStation, 0, 0, body))
}

func (host *virtualHost) sendProbesLocked() {
	for i := 0; i < 3; i++ {
		body := binary.BigEndian.AppendUint32(nil, host.constID)
		body = append(body, 0, 0, 0, 0)
		body = binary.BigEndian.AppendUint64(body, host.millis()+uint64(i))
		host.sendLocked(0, host.unconnected(piaProtoNAT, 1, piaFlagProbe, body))
	}
}

// * 持ち主は、サーバーから頼まれて先に確認を送る。本物もこの順番で、参加者はそれを受けてから送ってくる
func (host *virtualHost) startProbing(targets []*net.UDPAddr) {
	host.mutex.Lock()
	defer host.mutex.Unlock()

	host.targets = targets
	host.probeEnd = time.Now().Add(20 * time.Second)
	host.probeTargetsLocked()
	fmt.Printf("[YW2-VHOST] probing %v\n", targets)
}

func (host *virtualHost) probeTargetsLocked() {
	host.probeAt = time.Now()
	for _, target := range host.targets {
		for i := 0; i < 3; i++ {
			body := binary.BigEndian.AppendUint32(nil, host.constID)
			body = append(body, 0, 0, 0, 0)
			body = binary.BigEndian.AppendUint64(body, host.millis()+uint64(i))
			packet := buildPiaPacket(host.key, 0, 0, uint16(host.millis()), 0, host.unconnected(piaProtoNAT, 1, piaFlagProbe, body))
			if _, err := host.socket.WriteToUDP(packet, target); err != nil {
				fmt.Printf("[YW2-VHOST] probe failed: %v\n", err)
			}
		}
	}
}

func (host *virtualHost) sendGameLocked(kind uint16, data []byte) {
	host.seq += 2
	sent := &vhostSent{seq: host.seq, kind: kind, data: data, sentAt: time.Now()}
	host.sent = append(host.sent, sent)
	host.writeGameLocked(sent)
}

func (host *virtualHost) writeGameLocked(sent *vhostSent) {
	host.sendLocked(host.connID, host.connected(piaProtoGame, 0, vhostDestPeer, buildPiaGame(sent.seq, host.received, sent.kind, sent.data)))
}

func (host *virtualHost) sendAckLocked() {
	data := binary.LittleEndian.AppendUint32(nil, host.ackTick)
	host.ackTick++
	host.sendLocked(host.connID, host.connected(piaProtoGame, 0, vhostDestPeer, buildPiaGame(host.seq+1, host.received, piaKindAck, data)))
}

func (host *virtualHost) sendMeshLocked() {
	body := binary.BigEndian.AppendUint32(nil, 0x0003007C)
	body = binary.BigEndian.AppendUint32(body, 0)
	body = binary.BigEndian.AppendUint32(body, host.meshCounter)
	body = binary.BigEndian.AppendUint32(body, 0xFFFFF82F)
	body = append(body, make([]byte, 8)...)
	body = append(body, 0x20, 0x02, 0, 0, 0, 0, 0, 0x01, 0x01, 0x00, 0x02, 0x00)
	body = append(body, host.addressBlocks()...)
	body = append(body, 0, 0)
	body = append(body, host.blocks...)
	body = append(body, 0x01, 0x00)
	host.lastMesh = time.Now()
	host.sendLocked(host.connID, host.connected(piaProtoMesh, 1, vhostDestPeer, body))
}

func (host *virtualHost) receive() {
	buffer := make([]byte, 2048)
	for {
		size, source, err := host.socket.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		raw := append([]byte(nil), buffer[:size]...)
		safely("vhost packet", func() { host.handle(raw, source) })
	}
}

func (host *virtualHost) handle(raw []byte, source *net.UDPAddr) {
	host.mutex.Lock()
	defer host.mutex.Unlock()

	if host.key == nil {
		return
	}

	_, _, srcTimer, messages, ok := parsePiaPacket(raw)
	if !ok {
		fmt.Printf("[YW2-VHOST] cannot read packet from %s: %x\n", source, raw)
		return
	}
	end := len(raw) - piaSignatureSize
	if !hmac.Equal(piaSignature(raw[:end], host.key), raw[end:]) {
		fmt.Printf("[YW2-VHOST] signature differs from %s\n", source)
		return
	}

	if host.peer == nil || !sameAddress(host.peer, source) {
		fmt.Printf("[YW2-VHOST] peer %s\n", source)
		host.peer = source
	}
	host.peerTimer = srcTimer
	host.peerAt = time.Now()
	// * 本物の確認は接続の前に終わる。相手から届いたら、もう送らない
	host.probeEnd = time.Time{}

	for _, message := range messages {
		host.peerConst = message.Src
		if message.Station == 1 && !host.peerReady {
			host.peerReady = true
			fmt.Printf("[YW2-VHOST] side %d ready after joining\n", host.side)
		}
		switch message.Proto {
		case piaProtoNAT:
			host.handleNAT(message)
		case piaProtoStation:
			host.handleStation(message)
		case piaProtoMesh:
			host.handleMesh(message)
		case piaProtoClock:
			// * 部屋に入るまでは接続の通信を送らない
			if host.joined && len(message.Body) >= 8 {
				body := append([]byte(nil), message.Body[:8]...)
				body = binary.BigEndian.AppendUint64(body, host.millis())
				host.sendLocked(host.connID, host.connected(piaProtoClock, 0, vhostDestPeer, body))

				// * 本物は、最初の時刻合わせに答えてから部屋の情報を送る
				if host.joined && !host.meshStarted {
					host.meshStarted = true
					host.meshSince = time.Now()
					host.sendMeshLocked()
				}
			}
		case piaProtoKeepAlive:
			if host.joined && len(message.Body) == 16 && binary.BigEndian.Uint32(message.Body) == 0 {
				body := append([]byte{0, 0, 0, 1}, message.Body[4:]...)
				host.sendLocked(host.connID, host.connected(piaProtoKeepAlive, 0, vhostDestPeer, body))
			}
		case piaProtoGame:
			if game, ok := parsePiaGame(message.Body); ok {
				host.handleGame(game)
			}
		default:
			fmt.Printf("[YW2-VHOST] unknown protocol %04x/%d: %x\n", message.Proto, message.Port, message.Body)
		}
	}
}

func (host *virtualHost) handleNAT(message piaMessage) {
	if message.Port != 1 || len(message.Body) != 16 {
		return
	}

	body := binary.BigEndian.AppendUint32(nil, host.constID)
	body = append(body, 1, 0, 0, 0)
	body = append(body, message.Body[8:]...)
	host.sendLocked(0, host.unconnected(piaProtoNAT, 2, piaFlagProbe, body))

	if !host.probed {
		host.probed = true
		fmt.Printf("[YW2-VHOST] probe from peer %d\n", message.Src)
		host.sendProbesLocked()
	}
}

func (host *virtualHost) handleStation(message piaMessage) {
	body := message.Body
	if len(body) < 8 {
		return
	}

	nonce := body[len(body)-4:]
	switch body[0] {
	case 0x01:
		if len(body) != 62 {
			fmt.Printf("[YW2-VHOST] connection request of size %d\n", len(body))
			return
		}
		host.peerConn = body[1]
		host.blocks = append([]byte(nil), body[4:58]...)
		host.ackStationLocked(nonce)
		if !host.seenKinds[0x0101] {
			host.seenKinds[0x0101] = true
			fmt.Printf("[YW2-VHOST] connection request, peer connection %02x\n", host.peerConn)
			request := append([]byte{0x01, host.connID, 0x03, 0x01}, host.addressBlocks()...)
			host.sendPendingLocked(0, piaProtoStation, 0, 0, request)
		}
	case 0x02:
		host.ackStationLocked(nonce)
		if !host.seenKinds[0x0102] {
			host.seenKinds[0x0102] = true
			fmt.Printf("[YW2-VHOST] connection response\n")
			response := append([]byte{0x02, 0x00, 0x03, 0x02}, make([]byte, 68)...)
			host.sendPendingLocked(0, piaProtoStation, 0, 0, response)
		}
	case 0x05:
		value := binary.BigEndian.Uint32(nonce)
		kept := host.pending[:0]
		for _, pending := range host.pending {
			if pending.nonce != value {
				kept = append(kept, pending)
			}
		}
		host.pending = kept
	}
}

func (host *virtualHost) handleMesh(message piaMessage) {
	// * 部屋の情報の更新への返事。返事が来るまでは同じ番号で送り直す
	if message.Port == 1 {
		if !host.meshAcked {
			host.meshAcked = true
			host.meshDone = true
			host.meshCounter++
			fmt.Printf("[YW2-VHOST] mesh update answered\n")
		}
		return
	}

	if message.Port != 0 || len(message.Body) < 2 {
		return
	}

	switch message.Body[0] {
	case 0x01:
		if len(message.Body) < 16 {
			return
		}
		host.ackStationLocked(message.Body[len(message.Body)-4:])
		if host.joined {
			return
		}
		host.joined = true
		fmt.Printf("[YW2-VHOST] join request\n")

		body := []byte{0x02, 0x02, 0x00, 0x01, 0x01, 0x00, 0x02, 0x00}
		body = append(body, host.addressBlocks()...)
		body = append(body, 0, 0)
		body = append(body, host.blocks...)
		body = append(body, 0x01, 0x00)

		// * 本物は、接続後の最初のパケット（番号 1）にこの 3 つをまとめて入れる
		host.seq += 2
		start := make([]byte, 4)
		host.sent = append(host.sent, &vhostSent{seq: host.seq, kind: piaKindStart, data: start, sentAt: time.Now()})
		ack := binary.LittleEndian.AppendUint32(nil, host.ackTick)
		host.ackTick++
		host.nonce++
		full := binary.BigEndian.AppendUint32(body, host.nonce)
		host.pending = append(host.pending, &vhostPending{conn: host.connID, nonce: host.nonce, proto: piaProtoMesh, body: full, sentAt: time.Now()})
		host.sendLocked(host.connID,
			host.connected(piaProtoGame, 0, vhostDestPeer, buildPiaGame(host.seq, host.received, piaKindStart, start)),
			host.connected(piaProtoGame, 0, vhostDestPeer, buildPiaGame(host.seq+1, host.received, piaKindAck, ack)),
			piaMessage{Src: host.constID, Proto: piaProtoMesh, Body: full},
		)
	case 0x11:
		fmt.Printf("[YW2-VHOST] side %d left\n", host.side)
		host.bridge.onLeft(host.side)
		host.resetLocked()
	}
}

func (host *virtualHost) handleGame(game piaGame) {
	// * 相手が受け取った数の分は、送り直さない
	kept := host.sent[:0]
	for n, sent := range host.sent {
		if uint32(sent.seq/2) > game.Ack {
			kept = append(kept, host.sent[n])
		}
	}
	host.sent = kept

	if game.Kind == piaKindAck {
		return
	}
	if game.Seq%2 != 0 || game.Seq <= host.lastPeerSeq {
		return
	}
	host.lastPeerSeq = game.Seq
	host.received++

	if !host.seenKinds[game.Kind] {
		host.seenKinds[game.Kind] = true
		fmt.Printf("[YW2-VHOST] side %d sent %04x seq %d len %d\n", host.side, game.Kind, game.Seq, len(game.Data))
	}

	// * 操作の通信を読むための記録。本人の状態の報告とパーティは大きいので除く
	if game.Kind != piaKindState && game.Kind != piaKindParty {
		fmt.Printf("[YW2-VHOST-MSG] side %d %04x %x\n", host.side, game.Kind, game.Data)
	}

	host.bridge.onGame(host.side, game.Kind, game.Data)
}

func (host *virtualHost) tick() {
	for range time.Tick(100 * time.Millisecond) {
		host.mutex.Lock()
		host.tickLocked(time.Now())
		host.mutex.Unlock()
	}
}

func (host *virtualHost) tickLocked(now time.Time) {
	if host.key != nil && !host.joined && now.Before(host.probeEnd) && now.Sub(host.probeAt) >= 500*time.Millisecond {
		host.probeTargetsLocked()
	}

	if host.key == nil || host.peer == nil {
		return
	}

	for _, pending := range host.pending {
		if now.Sub(pending.sentAt) >= 500*time.Millisecond {
			pending.sentAt = now
			host.sendLocked(pending.conn, piaMessage{Src: host.constID, Proto: pending.proto, Body: pending.body})
		}
	}

	// * 本物は部屋に入る応答のあと、相手から届くまで何も送らない。先に送ると相手が受け取らなくなることがあった
	if !host.joined || !host.peerReady {
		return
	}

	if host.meshStarted {
		wait := 10 * time.Second
		if !host.meshAcked {
			wait = 200 * time.Millisecond
		}
		if now.Sub(host.lastMesh) >= wait {
			host.meshAcked = false
			host.sendMeshLocked()
		}
	}

	// * 本物は、最初の部屋の情報に返事が来るまで、ほかをほとんど送らない。
	// * 更新のすぐあとに確認や生存確認が届くと、本体が先へ進まなくなることがあった
	if !host.meshDone {
		if !host.stuckLog && host.meshStarted && now.Sub(host.meshSince) >= 5*time.Second {
			host.stuckLog = true
			fmt.Printf("[YW2-VHOST] side %d STUCK: no answer to the mesh update\n", host.side)
		}
		return
	}

	for _, sent := range host.sent {
		if now.Sub(sent.sentAt) >= time.Second {
			sent.sentAt = now
			host.writeGameLocked(sent)
		}
	}
	if now.Sub(host.lastAck) >= 500*time.Millisecond {
		host.lastAck = now
		host.sendAckLocked()
	}
	if now.Sub(host.lastPing) >= 200*time.Millisecond {
		host.lastPing = now
		body := make([]byte, 8)
		body = binary.BigEndian.AppendUint64(body, host.millis())
		host.sendLocked(host.connID, host.connected(piaProtoKeepAlive, 0, vhostDestPeer, body))
	}
}

// * 偽のプレイヤーが持ち主の部屋を作る。部屋は 1 人に 1 つ
func (host *virtualHost) prepare(bot uint32, player uint32) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}

	database := globals.Postgres

	var gid uint32
	err := database.QueryRow(`
		INSERT INTO matchmaking.gatherings (owner_pid, host_pid, min_participants, max_participants, participation_policy, policy_argument, flags, state, description, registered, type, started_time, participants)
		VALUES ($1, $1, 1, 2, 98, 0, 0, 0, '', true, 'MatchmakeSession', now(), ARRAY[$1::numeric])
		RETURNING id`, bot).Scan(&gid)
	if err != nil {
		return err
	}

	// * 部屋の設定は、本人が作った部屋をまねる
	_, err = database.Exec(`
		INSERT INTO matchmaking.matchmake_sessions (id, game_mode, attribs, open_participation, matchmake_system_type, application_buffer, flags, state, progress_score, session_key, option_zero, matchmake_param, user_password, refer_gid, user_password_enabled, system_password_enabled, codeword, system_password)
		SELECT $1, m.game_mode, m.attribs, true, m.matchmake_system_type, m.application_buffer, m.flags, m.state, m.progress_score, $2, m.option_zero, m.matchmake_param, m.user_password, 0, false, false, m.codeword, m.system_password
		FROM matchmaking.matchmake_sessions m JOIN matchmaking.gatherings g ON g.id = m.id
		WHERE g.owner_pid = $3 AND m.game_mode = 2 ORDER BY m.id DESC LIMIT 1`, gid, key, player)
	if err != nil {
		return err
	}

	host.mutex.Lock()
	host.key = key
	host.bot = bot
	host.gid = gid
	host.constID += 2 // * 持ち主の RVCID も部屋ごとに変える
	host.resetLocked()
	host.bridge.onPrepared(host.side)
	host.mutex.Unlock()

	globals.SetEventVirtualHost(player, bot, gid)
	fmt.Printf("[YW2-VHOST] room %d ready for %d\n", gid, player)
	return nil
}

func (host *virtualHost) isRoom(gid uint32) bool {
	host.mutex.Lock()
	defer host.mutex.Unlock()

	return host.key != nil && host.gid == gid && !host.silent
}

func (host *virtualHost) stationURL() string {
	host.mutex.Lock()
	defer host.mutex.Unlock()

	return fmt.Sprintf("prudp:/address=%s;port=%d;natf=0;natm=0;pmp=0;sid=15;upnp=0;type=3;PID=%d;RVCID=%d", host.publicIP, host.port, host.bot, host.constID)
}

func registerVirtualHostURLs(protocol *match_making.Protocol) {
	original := protocol.GetSessionURLs
	protocol.GetSessionURLs = func(err error, packet nex.PacketInterface, callID uint32, gid types.UInt32) (*nex.RMCMessage, *nex.Error) {
		host := vhostOfRoom(uint32(gid))
		if err != nil || host == nil {
			return original(err, packet, callID, gid)
		}

		connection := packet.Sender().(*nex.PRUDPConnection)
		endpoint := connection.Endpoint().(*nex.PRUDPEndPoint)
		url := host.stationURL()
		fmt.Printf("[YW2-VHOST] host url %s\n", url)

		stream := nex.NewByteStreamOut(endpoint.LibraryVersions(), endpoint.ByteStreamSettings())
		urls := types.NewList[types.StationURL]()
		urls = append(urls, types.NewStationURL(types.NewString(url)))
		urls.WriteTo(stream)

		response := nex.NewRMCSuccess(endpoint, stream.Bytes())
		response.ProtocolID = match_making.ProtocolID
		response.MethodID = match_making.MethodGetSessionURLs
		response.CallID = callID
		return response, nil
	}
}
