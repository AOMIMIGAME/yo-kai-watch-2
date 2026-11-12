package nex

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPiaPacketMatchesRecord(t *testing.T) {
	// * 実際の通信の先頭（確認の依頼）。署名の 16 バイトは別の鍵なので比べない
	record, _ := hex.DecodeString("32ab98640100000018ec0000080000100000000000000004040000010000000000000004000000000000064a693e9be2e4db39ca56b731fa41778d7f153aed5c")
	conn, id, timer, messages, ok := parsePiaPacket(record)
	if !ok || conn != 0 || id != 0 || timer != 0x18ec || len(messages) != 1 {
		t.Fatalf("parse: %d %d %x %d %v", conn, id, timer, len(messages), ok)
	}
	message := messages[0]
	if message.Flags != piaFlagProbe || message.Src != 4 || message.Proto != piaProtoNAT || message.Port != 1 || len(message.Body) != 16 {
		t.Fatalf("message: %+v", message)
	}

	built := buildPiaPacket([]byte("key"), 0, 0, 0x18ec, 0, message)
	if !bytes.Equal(built[:len(built)-16], record[:len(record)-16]) {
		t.Fatalf("built %x", built)
	}
}

func TestPiaGameMatchesRecord(t *testing.T) {
	// * 記録: 番号 2、種類 f00a、確かめ用 09
	want, _ := hex.DecodeString("4c4b816f020000440000000001000af009f00044040000000000000" + "04c4b816f")
	built := buildPiaGame(2, 0, piaKindStart, make([]byte, 4))
	if !bytes.Equal(built, want) {
		t.Fatalf("got %x, want %x", built, want)
	}
	game, ok := parsePiaGame(built)
	if !ok || game.Seq != 2 || game.Kind != piaKindStart || len(game.Data) != 4 {
		t.Fatalf("parse %+v %v", game, ok)
	}
	if built := buildPiaGame(0x36, 0, 0x1010, make([]byte, 4)); built[16] != 0x27 {
		t.Fatalf("check byte %x", built[16])
	}
}

func TestBridgeFormsMatchRecord(t *testing.T) {
	// * 記録: 0x1003 の 04100500、位置 08 02
	turn := buildTurn(8, 2)
	if len(turn) != 96 || turn[4] != 8 || !bytes.Equal(turn[24:28], []byte{0x04, 0x10, 0x05, 0x00}) || turn[32] != 8 || turn[33] != 2 {
		t.Fatalf("turn %x", turn)
	}

	// * 記録: n=5、技 3032d42e、行動 04100500（08 02）、相手は位置 00 01、末尾 60 00 01 00
	hash, _ := hex.DecodeString("3032d42e")
	order := buildOrder(5, hash, 8, 2, 0, 1, []byte{1, 2, 3, 4})
	if len(order) != 88 || order[0] != 5 || !bytes.Equal(order[8:12], hash) || !bytes.Equal(order[16:20], []byte{0x04, 0x10, 0x05, 0x00}) {
		t.Fatalf("order %x", order)
	}
	if !bytes.Equal(order[24:28], []byte{8, 2, 0, 1}) || !bytes.Equal(order[64:68], []byte{1, 2, 3, 4}) || !bytes.Equal(order[84:88], []byte{0x60, 0, 1, 0}) {
		t.Fatalf("order fields %x", order)
	}

	// * 相手の妖怪の行動は末尾が 60 00 00 00
	if other := buildOrder(7, hash, 1, 1, 7, 2, nil); !bytes.Equal(other[84:88], []byte{0x60, 0, 0, 0}) || other[32] != 0 {
		t.Fatalf("host order %x", other)
	}

	// * 付加情報。記録: 要求のピンは番号 03103c00、位置 01 01。[52] の印は確定の [32] へ
	request := make([]byte, 96)
	copy(request[68:], []byte{0x03, 0x10, 0x3C, 0x00, 1, 1})
	request[52] = 0x10
	own := buildOrder(6, hash, 8, 2, 1, 1, nil)
	seen := buildOrder(6, hash, 2, 1, 7, 2, nil)
	copyActionDetails(own, seen, request)
	if own[32] != 0x10 || seen[32] != 0x10 {
		t.Fatalf("mark %x %x", own[32], seen[32])
	}
	// * 位置 1/1 は持ち主の数え方で (0x81, 0x83)。相手から見れば位置 7/2
	if !bytes.Equal(own[56:62], []byte{0x81, 0, 0x83, 0, 1, 1}) || !bytes.Equal(seen[56:62], []byte{0x03, 0x10, 0x04, 0x00, 7, 2}) {
		t.Fatalf("pin %x %x", own[56:62], seen[56:62])
	}
	none := buildOrder(6, hash, 8, 2, 1, 1, nil)
	copyActionDetails(none, none, make([]byte, 96))
	if !bytes.Equal(none[56:62], make([]byte, 6)) {
		t.Fatalf("no pin %x", none[56:62])
	}

	for _, test := range [][4]byte{{8, 2, 2, 1}, {6, 2, 0, 1}, {11, 2, 5, 1}, {0, 1, 6, 2}, {5, 1, 11, 2}} {
		index, team := mirrorSlot(test[0], test[1])
		if index != test[2] || team != test[3] {
			t.Fatalf("mirror %v: %d %d", test, index, team)
		}
	}
}

func TestJoinResponseIsFirstPacket(t *testing.T) {
	bridge := newVhostBridge()
	host, err := newVirtualHost("127.0.0.1", relayTestFirstPort+80, 0, bridge)
	if err != nil {
		t.Fatal(err)
	}
	defer host.socket.Close()

	key := []byte("0123456789abcdef0123456789abcdef")
	bridge.mutex.Lock()
	host.key = key
	host.bot = 1999999001
	bridge.mutex.Unlock()

	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	peer := &vhostTestPeer{t: t, socket: socket, host: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: relayTestFirstPort + 80}, key: key}

	// * 部屋に入る前の生存確認には答えない
	peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoKeepAlive, Body: make([]byte, 16)})
	request := append([]byte{0x01, 0x66, 0x03, 0x00}, bytes.Repeat([]byte{0xAA}, 54)...)
	request = append(request, 1, 2, 3, 4)
	peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoStation, Body: request})
	peer.send(piaMessage{Station: 0xFD, Src: 9, Proto: piaProtoMesh, Body: []byte{0x01, 0xFD, 0, 0, 1, 2, 3, 4, 5, 6, 0, 0, 9, 9, 9, 9}})

	buffer := make([]byte, 2048)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		socket.SetReadDeadline(deadline)
		size, _, err := socket.ReadFromUDP(buffer)
		if err != nil {
			t.Fatal(err)
		}
		conn, id, _, messages, ok := parsePiaPacket(buffer[:size])
		if !ok || conn != host.connID {
			continue
		}
		// * 宛先は相手の RVCID（9）ではなく、ステーション番号のビット
		if messages[0].Dest != vhostDestPeer || messages[1].Dest != vhostDestPeer {
			t.Fatalf("dest %d %d", messages[0].Dest, messages[1].Dest)
		}
		if id != 1 || len(messages) != 3 || messages[0].Proto != piaProtoGame || messages[1].Proto != piaProtoGame || messages[2].Proto != piaProtoMesh || len(messages[2].Body) != 124 {
			t.Fatalf("first packet id %d: %+v", id, messages)
		}

		// * 確認の返事を返すと、相手が動くまで何も届かない
		peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoStation, Body: append([]byte{0x05, 0, 0, 0}, messages[2].Body[120:]...)})
		socket.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
		for {
			size, _, err := socket.ReadFromUDP(buffer)
			if err != nil {
				break
			}
			if conn, _, _, _, ok := parsePiaPacket(buffer[:size]); ok && conn == host.connID {
				t.Fatalf("sent before the peer was ready: %x", buffer[:size])
			}
		}

		// * 時刻合わせのあとは、部屋の情報の送り直しだけが届く
		peer.send(piaMessage{Station: 1, Dest: 1, Src: 2, Proto: piaProtoClock, Body: make([]byte, 16)})
		quietUntil := time.Now().Add(800 * time.Millisecond)
		meshes := 0
		for time.Now().Before(quietUntil) {
			socket.SetReadDeadline(quietUntil)
			size, _, err := socket.ReadFromUDP(buffer)
			if err != nil {
				break
			}
			_, _, _, messages, ok := parsePiaPacket(buffer[:size])
			if !ok {
				continue
			}
			for _, message := range messages {
				switch message.Proto {
				case piaProtoMesh:
					meshes++
				case piaProtoClock, piaProtoStation:
				default:
					t.Fatalf("sent %04x before the mesh answer", message.Proto)
				}
			}
		}
		if meshes < 2 {
			t.Fatalf("mesh sent %d times", meshes)
		}

		// * 返事が来たら、生存確認が始まる
		peer.send(piaMessage{Station: 1, Dest: 1, Src: 2, Proto: piaProtoMesh, Port: 1, Body: make([]byte, 24)})
		peer.wait("keep alive", func(message piaMessage) bool { return message.Proto == piaProtoKeepAlive })
		return
	}
	t.Fatal("no first packet")
}

func TestResetClearsMeshState(t *testing.T) {
	bridge := newVhostBridge()
	host := &virtualHost{mutex: &bridge.mutex, bridge: bridge}
	host.meshDone, host.meshStarted, host.meshAcked, host.peerReady, host.joined = true, true, true, true, true
	host.resetLocked()
	if host.meshDone || host.meshStarted || host.meshAcked || host.peerReady || host.joined {
		t.Fatalf("not cleared: %+v", host)
	}
}

func TestConnectionIDChanges(t *testing.T) {
	bridge := newVhostBridge()
	host := &virtualHost{mutex: &bridge.mutex, bridge: bridge}
	seen := map[byte]bool{}
	for i := 0; i < 50; i++ {
		host.resetLocked()
		if host.connID < 2 {
			t.Fatalf("connection id %d", host.connID)
		}
		seen[host.connID] = true
	}
	if len(seen) < 10 {
		t.Fatalf("only %d values", len(seen))
	}
}

func TestFrontFromState(t *testing.T) {
	cases := []struct {
		flags []byte
		front int
		ok    bool
	}{
		{[]byte{0, 0, 0, 16, 16, 16}, 0, true},
		{[]byte{16, 0, 0, 0, 16, 16}, 1, true},
		{[]byte{16, 16, 0, 0, 0, 16}, 2, true},
		{[]byte{16, 16, 16, 0, 0, 0}, 3, true},
		{[]byte{0, 16, 16, 16, 0, 0}, 4, true},
		{[]byte{17, 17, 17, 0, 0, 1}, 3, true},
		{[]byte{0, 0, 1, 16, 16, 16}, 0, true},
		{[]byte{0, 0, 0, 0, 0, 0}, 0, false},
		{[]byte{0, 16, 0, 16, 0, 16}, 0, false},
	}
	for _, test := range cases {
		data := make([]byte, bridgeStateSize)
		for n, flag := range test.flags {
			data[n*piaStateEntrySize+bridgeStateRow] = flag
		}
		front, ok := frontFromState(data)
		if ok != test.ok || (ok && front != test.front) {
			t.Errorf("%v: %d %v", test.flags, front, ok)
		}
	}
}

func TestPassDoesNotOrder(t *testing.T) {
	bridge := newVhostBridge()
	bridge.running, bridge.waitAction, bridge.actingSide = true, true, 0
	bridge.n = 9
	action := make([]byte, 96)
	action[32], action[33] = 6, 2

	// * 技の値が 0 なら、通し番号は進まない（本体がないので、確定を送ると落ちる）
	bridge.onAction(0, action)
	if bridge.n != 9 || bridge.waitAction || bridge.waitState || bridge.nextAt.IsZero() {
		t.Fatalf("n %d wait %v %v", bridge.n, bridge.waitAction, bridge.waitState)
	}
}

func TestRotateFront(t *testing.T) {
	// * 値が回したあとの前の先頭。Ｅの記録: 前 0,1,2 で値 3 → 3,4,5。前 3,4,5 で値 0 → 0,1,2
	if got := rotateFront(0, 3); got != 3 {
		t.Fatalf("three steps %d", got)
	}
	if got := rotateFront(3, 0); got != 0 {
		t.Fatalf("back to zero %d", got)
	}
	if got := rotateFront(0, 1); got != 1 {
		t.Fatalf("one step %d", got)
	}
}

func TestBuildOperationMatchesRecord(t *testing.T) {
	// * 記録: 要求 b0aaf088、本体の番号 9300 9500、位置 00 00 09 02、値 3、末尾 09 00 01 00
	// * 持ち主の確定は番号が 0510 2600 に変わる
	request := make([]byte, bridgeOrderSize)
	copy(request[8:], []byte{0xB0, 0xAA, 0xF0, 0x88})
	copy(request[20:], []byte{0x93, 0x00, 0x95, 0x00})
	request[26], request[27] = 9, 2
	request[72] = 3
	request[84], request[86] = 9, 1

	own := buildOperation(9, request, false, []byte{1, 2, 3, 4})
	if own[0] != 9 || !bytes.Equal(own[20:24], []byte{0x05, 0x10, 0x06, 0x00}) || !bytes.Equal(own[16:20], make([]byte, 4)) {
		t.Fatalf("own %x", own)
	}
	if !bytes.Equal(own[24:28], []byte{0, 0, 9, 2}) || own[72] != 3 || !bytes.Equal(own[84:88], []byte{9, 0, 1, 0}) || !bytes.Equal(own[64:68], []byte{1, 2, 3, 4}) {
		t.Fatalf("own fields %x", own)
	}

	other := buildOperation(9, request, true, []byte{1, 2, 3, 4})
	if !bytes.Equal(other[24:28], []byte{0, 0, 3, 1}) || !bytes.Equal(other[84:88], []byte{9, 0, 0, 0}) || other[72] != 3 || !bytes.Equal(other[64:68], []byte{1, 2, 3, 4}) {
		t.Fatalf("other %x", other)
	}

	// * G 技。記録: 位置 7/2 の番号 97009900 が 03100400 になる
	gMove := make([]byte, bridgeOrderSize)
	copy(gMove[8:], []byte{0x67, 0xAB, 0xEC, 0xE6})
	copy(gMove[16:], []byte{0x97, 0, 0x99, 0, 0x97, 0, 0x99, 0, 7, 2, 7, 2})
	copy(gMove[36:], []byte{0x97, 0, 0x99, 0, 7, 2})
	if got := buildOperation(1, gMove, false, nil); !bytes.Equal(got[36:42], []byte{0x03, 0x10, 0x04, 0x00, 7, 2}) {
		t.Fatalf("g move %x", got[36:42])
	}
	if got := buildOperation(1, gMove, true, nil); !bytes.Equal(got[36:42], []byte{0x81, 0, 0x83, 0, 1, 1}) {
		t.Fatalf("g move seen %x", got[36:42])
	}

	// * つつく。記録: 指は番号 00100100、位置 0c 01 のまま。相手の番号だけ直す
	poke := make([]byte, bridgeOrderSize)
	copy(poke[8:], []byte{0xC0, 0xFB, 0xE4, 0x1C})
	copy(poke[16:], []byte{0x00, 0x10, 0x01, 0x00, 0x05, 0x10, 0x06, 0x00, 12, 1, 3, 1})
	got := buildOperation(1, poke, false, nil)
	if !bytes.Equal(got[16:20], []byte{0x00, 0x10, 0x01, 0x00}) || !bytes.Equal(got[20:24], []byte{0x83, 0, 0x85, 0}) || !bytes.Equal(got[24:28], []byte{12, 1, 3, 1}) {
		t.Fatalf("poke %x", got[16:28])
	}

	// * 必殺技の印はそのまま残し、相手から見れば 0
	special := append([]byte(nil), request...)
	special[84], special[86] = 1, 0x21
	if got := buildOperation(9, special, false, nil); got[86] != 0x21 {
		t.Fatalf("own mark %x", got[86])
	}
	if got := buildOperation(9, special, true, nil); got[86] != 0x20 {
		t.Fatalf("seen mark %x", got[86])
	}

	// * 要求そのものは変えない
	if request[26] != 9 || request[0] != 0 {
		t.Fatalf("request changed %x", request)
	}

	// * おはらい: 自分が自分へ
	purify := make([]byte, bridgeOrderSize)
	copy(purify[8:], []byte{0xDD, 0xFA, 0xE5, 0x7F})
	copy(purify[16:], []byte{0x97, 0, 0x99, 0, 0x97, 0, 0x99, 0, 7, 2, 7, 2})
	seen := buildOperation(1, purify, true, nil)
	if !bytes.Equal(seen[24:28], []byte{1, 1, 1, 1}) || !bytes.Equal(seen[16:20], seen[20:24]) {
		t.Fatalf("purify %x", seen)
	}
}

func sameInts(one []int, two ...int) bool {
	if len(one) != len(two) {
		return false
	}
	for n := range one {
		if one[n] != two[n] {
			return false
		}
	}
	return true
}

func TestEligibleSkipsCharging(t *testing.T) {
	bridge := newVhostBridge()
	bridge.state[0].hp = readStateHP(testState(1, []uint16{9, 9, 9, 9, 9, 9}, []uint16{9, 9, 9, 9, 9, 9}))

	// * 記録: 溜めている間の印 [16, 34, 2, 2, 16, 16]。前は 1,2,3 で全員溜めている
	bridge.front[0] = 1
	bridge.state[0].flags = []uint32{16, 34, 2, 2, 16, 16}
	if got := bridge.eligible(0); len(got) != 0 {
		t.Fatalf("gave a turn to %v", got)
	}

	// * 溜めが終わると番が戻る。記録: [16, 32, 0, 0, 16, 16]
	bridge.state[0].flags = []uint32{16, 32, 0, 0, 16, 16}
	if got := bridge.eligible(0); !sameInts(got, 1, 2, 3) {
		t.Fatalf("after the charge %v", got)
	}

	// * 1 体だけ溜めていれば、ほかの妖怪に回す
	bridge.state[0].flags = []uint32{16, 2, 0, 0, 16, 16}
	if got := bridge.eligible(0); !sameInts(got, 2, 3) {
		t.Fatalf("skip %v", got)
	}
}

func TestEligibleFollowsFront(t *testing.T) {
	bridge := newVhostBridge()
	bridge.front[0] = 1
	bridge.state[0].hp = readStateHP(testState(1, []uint16{9, 9, 0, 9, 9, 9}, []uint16{9, 9, 9, 9, 9, 9}))
	// * 前は 1,2,3。2 は倒れている
	if got := bridge.eligible(0); !sameInts(got, 1, 3) {
		t.Fatalf("order %v", got)
	}

	// * 前が全滅でも、生きている妖怪を返す
	bridge.state[0].hp = readStateHP(testState(1, []uint16{0, 0, 0, 0, 7, 0}, []uint16{9, 9, 9, 9, 9, 9}))
	if got := bridge.eligible(0); !sameInts(got, 4) {
		t.Fatalf("alive %v", got)
	}
}

func TestWaitRow(t *testing.T) {
	// * ゲームの表の、形が変わる段。素早さ以上の最初の段を使う
	for speed, want := range map[int][3]int{
		0:    {156, 187, 123},
		1:    {156, 187, 123},
		2:    {155, 186, 122},
		5:    {155, 186, 122},
		161:  {103, 134, 70},
		164:  {102, 132, 69},
		170:  {100, 128, 67},
		171:  {99, 126, 66},
		240:  {90, 108, 57},
		250:  {88, 106, 56},
		500:  {38, 56, 31},
		501:  {36, 54, 30},
		2000: {36, 54, 30},
	} {
		low, high, step := bridgeWaitRow(speed)
		if [3]int{low, high, step} != want {
			t.Errorf("speed %d: %d %d %d, want %v", speed, low, high, step, want)
		}
	}
}

func TestSpeedTakesTheLowerReport(t *testing.T) {
	bridge := newVhostBridge()
	if bridge.speedOf(0, 0) != bridgeDefaultSpeed {
		t.Fatal("default")
	}
	own := make([]int, 12)
	seen := make([]int, 12)
	own[2], seen[8] = 999, 120
	bridge.state[0].speed = own
	if bridge.speedOf(0, 2) != 999 {
		t.Fatal("one report")
	}
	bridge.state[1].speed = seen
	if bridge.speedOf(0, 2) != 120 {
		t.Fatalf("forged speed %d", bridge.speedOf(0, 2))
	}
}

func testSpeeds(bridge *vhostBridge, host []int, joiner []int) {
	own := append(append([]int(nil), host...), joiner...)
	seen := append(append([]int(nil), joiner...), host...)
	bridge.state[0].speed, bridge.state[1].speed = own, seen
}

func TestNextActorFollowsRecordedBattle(t *testing.T) {
	// * 実際の対戦の記録。チームは交互ではなく、待ち時間の順に動く
	bridge := newVhostBridge()
	testSpeeds(bridge, []int{126, 158, 135, 171, 171, 171}, []int{130, 104, 150, 150, 195, 174})
	bridge.state[0].hp = readStateHP(testState(1, []uint16{9, 9, 9, 9, 9, 9}, []uint16{9, 9, 9, 9, 9, 9}))
	bridge.state[1].hp = readStateHP(testState(1, []uint16{9, 9, 9, 9, 9, 9}, []uint16{9, 9, 9, 9, 9, 9}))
	bridge.startClocks()
	// * 最初の乱数は記録から分からないので、表の範囲で順番に合う値を入れる
	bridge.wait[0] = [6]int{139, 112, 142, -1, -1, -1}
	bridge.wait[1] = [6]int{127, 123, 126, -1, -1, -1}

	got := [][2]int{}
	for n := 0; n < 10; n++ {
		side, position, ok := bridge.nextActor()
		if !ok {
			t.Fatal("nobody can act")
		}
		got = append(got, [2]int{side, position})
		bridge.finishTurn(side, position)
		// * 記録ではここで参加者の 2 体が倒れる
		if n == 5 {
			bridge.state[1].hp[0], bridge.state[1].hp[1] = 0, 0
		}
	}
	want := [][2]int{{0, 1}, {1, 1}, {1, 2}, {1, 0}, {0, 0}, {0, 2}, {0, 1}, {1, 2}, {0, 2}, {0, 0}}
	for n := range want {
		if got[n] != want[n] {
			t.Fatalf("turn %d: %v, want %v", n, got, want)
		}
	}
}

func testCupBridge() *vhostBridge {
	bridge := newVhostBridge()
	testSpeeds(bridge, []int{146, 140, 145, 154, 154, 154}, []int{325, 343, 325, 288, 288, 288})
	full := []uint16{9, 9, 9, 9, 9, 9}
	bridge.state[0].hp = readStateHP(testState(1, full, full))
	bridge.state[1].hp = readStateHP(testState(1, full, full))
	bridge.state[0].units = []bridgeUnit{{house: 1}, {house: 1}, {house: 1}, {house: 1}, {house: 1}, {house: 1}}
	one := bridgeUnit{effect: speedEffect{House: 10}, house: 2}
	apelican := bridgeUnit{effect: speedEffect{Center: 30}, house: 2}
	umbrella := bridgeUnit{effect: speedEffect{BackRow: 5}, house: 2}
	bridge.state[1].units = []bridgeUnit{one, apelican, one, umbrella, umbrella, umbrella}
	return bridge
}

func TestCurrentSpeedFollowsSkills(t *testing.T) {
	// * 杯の試合。後列の傘 3 体で 15%、本家の特性 2 体で 20%、アペリカンは前の真ん中で 30%
	bridge := testCupBridge()
	for position, want := range []int{438, 565, 438} {
		if got := bridge.currentSpeed(1, position); got != want {
			t.Errorf("joiner %d: %d, want %d", position, got, want)
		}
	}
	// * 元祖の持ち主には効かない
	if got := bridge.currentSpeed(0, 0); got != 146 {
		t.Errorf("host %d", got)
	}

	// * 真ん中から外れる、傘が倒れる、特性の持ち主が倒れると下がる
	bridge.front[1] = 1
	if got := bridge.currentSpeed(1, 1); got == 565 {
		t.Errorf("not in the center %d", got)
	}
	bridge.front[1] = 0
	bridge.state[1].hp[3] = 0
	if got := bridge.currentSpeed(1, 0); got != 422 {
		t.Errorf("one umbrella down %d", got)
	}
	bridge.state[1].hp[0] = 0
	if got := bridge.currentSpeed(1, 2); got != 390 {
		t.Errorf("one house unit down %d", got)
	}
}

func TestSpeedSkillsReplayCupBattle(t *testing.T) {
	// * 杯の試合の順番。最初の残りは記録に合う値（表の範囲の乱数は通信に出ない）
	bridge := testCupBridge()
	bridge.startClocks()
	bridge.wait[0] = [6]int{128, 136, 118, -1, -1, -1}
	bridge.wait[1] = [6]int{53, 36, 56, -1, -1, -1}
	slots := []int{7, 6, 8, 2, 7, 0, 1, 6, 8, 7, 6, 8, 7, 2, 0, 1, 6, 8, 7, 7, 6, 8, 2, 7}
	for n, slot := range slots {
		side, position, ok := bridge.nextActor()
		if !ok {
			t.Fatal("nobody can act")
		}
		got := position
		if side == 1 {
			got += bridgeTeamMon
		}
		if got != slot {
			t.Fatalf("turn %d: slot %d, want %d", n, got, slot)
		}
		bridge.finishTurn(side, position)
	}
}

func TestFlashSoulGoesFirstOnce(t *testing.T) {
	first := bridgeFirstWait
	bridgeFirstWait = func(low int, high int) int { return low }
	defer func() { bridgeFirstWait = first }()

	bridge := testCupBridge()
	bridge.state[0].units[2].effect.First = true
	bridge.startClocks()

	// * 速さの差があっても、印のある持ち主の妖怪が先頭
	if side, position, ok := bridge.nextActor(); !ok || side != 0 || position != 2 {
		t.Fatalf("first %d %d %v", side, position, ok)
	}
	bridge.finishTurn(0, 2)
	// * 一度動くと印は消える。ほかの妖怪は先頭の残りの分だけ引かれて 0 になり、先に並んだ順に動く
	if bridge.goesFirst[0][2] {
		t.Fatal("mark stayed")
	}
	if side, position, ok := bridge.nextActor(); !ok || side != 0 || position != 0 {
		t.Fatalf("second %d %d %v", side, position, ok)
	}
}

func TestReadPartySkills(t *testing.T) {
	bridge := newVhostBridge()
	data := make([]byte, bridgeTeamMon*bridgePartyRecord)
	binary.LittleEndian.PutUint32(data[bridgePartySpecies:], 0x0d2f62b2)
	binary.LittleEndian.PutUint32(data[bridgePartyItem:], 0x412eaa16)
	binary.LittleEndian.PutUint16(data[bridgePartyHouse:], 0x1507)
	data[bridgePartySoulLevel] = 2
	bridge.readParty(0, data, historyTables{
		SpeedSpecies: map[string]speedEffect{"d2f62b2": {House: 10}},
		SpeedItems:   map[string][]speedEffect{"412eaa16": {{First: true}, {First: true, Flat: 4}}},
	})
	unit := bridge.unit(0, 0)
	if unit.effect.House != 10 || !unit.item.First || unit.item.Flat != 4 || unit.house != bridgeHouseMain {
		t.Fatalf("unit %+v", unit)
	}
}

func TestSpeedRulesFromCode(t *testing.T) {
	bridge := testCupBridge()
	bridge.stages = []int{10, 15, 20, 30, 40, 55, 70, 85, 100, 110, 120, 130, 140, 150, 160, 180, 200}
	base := bridge.currentSpeed(0, 0)

	// * 状態の段
	bridge.state[0].status = []uint32{bridgeStatusSpeedUp, 0, 0, 0, 0, 0}
	if got, want := bridge.currentSpeed(0, 0), base*120/100; got != want {
		t.Errorf("up %d, want %d", got, want)
	}
	bridge.state[0].status[0] = bridgeStatusSpeedDown
	if got, want := bridge.currentSpeed(0, 0), base+base*(70-100)/100; got != want {
		t.Errorf("down %d, want %d", got, want)
	}
	bridge.state[0].units[0].effect.Immune = true
	if got := bridge.currentSpeed(0, 0); got != base {
		t.Errorf("immune %d, want %d", got, base)
	}
	bridge.state[0].units[0].effect.Immune = false
	bridge.state[0].status[0] = 0

	// * 状態異常で遅くなる
	bridge.state[0].units[0].effect.StatusMinus = 10
	bridge.state[0].status[0] = 1
	if got := bridge.currentSpeed(0, 0); got != 131 { // * 146 - 14.6 の切り捨て
		t.Errorf("status minus %d", got)
	}
	bridge.state[0].units[0].effect.StatusMinus = 0
	bridge.state[0].status[0] = 0

	// * 隣と同じ特性
	bridge.state[0].units[0].effect.SameStage = 1
	bridge.state[0].units[1].effect.SameStage = 1
	if got, want := bridge.currentSpeed(0, 0), base+base*10/100; got != want {
		t.Errorf("same %d, want %d", got, want)
	}
	bridge.state[0].units[0].effect.SameStage = 0
	bridge.state[0].units[1].effect.SameStage = 0

	// * 持ち物の無効: 2 組目の素早さで、魂も効かない
	bridge.state[1].plainSpeed = []int{225, 243, 225, 238, 238, 238}
	bridge.state[1].units[0].item.Flat = 40
	bridge.state[0].units[2].effect.NoItems = true
	if got := bridge.currentSpeed(1, 1); got != int(243+243*65/100) {
		t.Errorf("no items %d", got)
	}
	bridge.state[0].units[2].effect.NoItems = false

	// * 特性の無効: 特性がなくなり、持ち物は残る
	bridge.state[0].units[2].effect.NoAbility = true
	if got := bridge.currentSpeed(1, 1); got != 343 {
		t.Errorf("no ability %d", got)
	}
}

func TestResetSkillRestartsAllies(t *testing.T) {
	bridge := testCupBridge()
	bridge.resetSkills = map[string]bool{"cb8b3e3c": true}
	bridge.startClocks()
	bridge.wait[1] = [6]int{5, 6, 7, -1, -1, -1}
	bridge.actingSkill = "cb8b3e3c"
	bridge.finishTurn(1, 1)
	if bridge.wait[1][0] != bridge.fullWait(1, 0) || bridge.wait[1][2] != bridge.fullWait(1, 2) || bridge.wait[1][3] != -1 {
		t.Fatalf("allies %v", bridge.wait[1])
	}
	bridge.wait[1][0] = 5
	bridge.finishTurn(1, 1)
	if bridge.wait[1][0] != 5 {
		t.Fatal("reset without the skill")
	}
}

func TestCannotActStopsTheWait(t *testing.T) {
	bridge := newVhostBridge()
	bridge.startClocks()
	bridge.wait[0] = [6]int{50, 200, 300, -1, -1, -1}
	bridge.wait[1] = [6]int{400, 400, 400, -1, -1, -1}
	// * 溜め以外の動けない状態も止まる。0x20 と 0x08 は動ける
	bridge.state[0].flags = []uint32{0x20, 0x4000, 0x08, 0x10, 0x10, 0x10}
	bridge.nextActor()
	if bridge.wait[0][1] != 200 || bridge.wait[0][2] != 250 {
		t.Fatalf("waits %v", bridge.wait[0])
	}
	// * +0x380 の 0x10000 の妖怪は 0x02 でも動ける
	bridge.state[0].extra = []uint32{0, 0x10000, 0, 0, 0, 0}
	bridge.state[0].flags[1] = 0x02
	if bridge.charging(0, 1) {
		t.Fatal("strong unit stopped")
	}
}

func TestShuffleFollowsRecord(t *testing.T) {
	// * 杯の試合の記録。シードから、持ち主の側を先に入れ替えると、最初の報告の並びになる
	random := newBridgeRandom(0xFA1C2EFE)
	host := []int{351, 502, 456, 317, 317, 317}
	joiner := []int{411, 411, 411, 411, 428, 411}
	apply := func(values []int, order []int) []int {
		out := make([]int, len(values))
		for n, from := range order {
			out[n] = values[from]
		}
		return out
	}
	if got := apply(host, random.shuffle(6)); !sameOrder(got, []int{351, 502, 317, 317, 317, 456}) {
		t.Fatalf("host %v", got)
	}
	if got := apply(joiner, random.shuffle(6)); !sameOrder(got, []int{411, 428, 411, 411, 411, 411}) {
		t.Fatalf("joiner %v", got)
	}
}

func TestShuffleSeedKeepsBothSidesTogether(t *testing.T) {
	for _, counts := range [][2]int{{6, 6}, {6, 5}} {
		seed, one, two, ok := findShuffleSeed(counts[0], counts[1], 1)
		if !ok {
			t.Fatalf("no seed for %v", counts)
		}
		// * 2 台の引く順は逆。どちらから引いても同じ並びになる
		first := newBridgeRandom(seed)
		if !sameOrder(first.shuffle(counts[0]), one) || !sameOrder(first.shuffle(counts[1]), two) {
			t.Fatal("first order")
		}
		second := newBridgeRandom(seed)
		if !sameOrder(second.shuffle(counts[1]), two) || !sameOrder(second.shuffle(counts[0]), one) {
			t.Fatal("second order")
		}
	}
}

func TestPrepareShuffleMovesUnits(t *testing.T) {
	bridge := newVhostBridge()
	for side := range bridge.state {
		bridge.state[side].units = make([]bridgeUnit, bridgeTeamMon)
		for position := range bridge.state[side].units {
			bridge.state[side].units[position] = bridgeUnit{present: true, tribe: position + 1}
		}
	}
	bridge.state[1].units[4].effect.Shuffle = true
	bridge.prepareShuffle()
	seed := binary.LittleEndian.Uint32(bridge.seed)
	order := newBridgeRandom(seed).shuffle(6)
	for position, from := range order {
		if bridge.state[0].units[position].tribe != from+1 {
			t.Fatalf("position %d: %+v, order %v", position, bridge.state[0].units[position], order)
		}
	}
	if !sameOrder(newBridgeRandom(seed).shuffle(6), order) {
		t.Fatal("seed")
	}
}

func TestTribeBonus(t *testing.T) {
	bridge := testCupBridge()
	bridge.tribeBonus = [][3]int{{4, 2, 10}, {4, 3, 15}}
	for position := range bridge.state[0].units {
		bridge.state[0].units[position].tribe = 4
	}
	// * 3 体そろって 15%。記録の 171 → 196
	bridge.state[0].units[0].house = 1
	testSpeeds(bridge, []int{171, 171, 171, 154, 154, 154}, []int{325, 343, 325, 288, 288, 288})
	if got := bridge.currentSpeed(0, 1); got != 196 {
		t.Fatalf("three %d", got)
	}
	// * 2 体なら 10%。真ん中が倒れると効かない。後列には効かない
	bridge.state[0].units[2].tribe = 3
	if got := bridge.currentSpeed(0, 1); got != 188 {
		t.Fatalf("two %d", got)
	}
	if got := bridge.currentSpeed(0, 4); got != 154 {
		t.Fatalf("back row %d", got)
	}
	bridge.state[0].hp[1] = 0
	if got := bridge.currentSpeed(0, 0); got != 171 {
		t.Fatalf("center down %d", got)
	}
}

func TestCheckTurnReports(t *testing.T) {
	bridge := newVhostBridge()
	bridge.n = 5
	if _, checked := bridge.checkTurnReports(); checked {
		t.Fatal("checked without reports")
	}

	one := []uint16{1, 2, 3, 4, 5, 6}
	two := []uint16{7, 8, 9, 10, 11, 12}
	bridge.state[0].hp = readStateHP(testState(1, one, two))
	bridge.state[1].hp = readStateHP(testState(1, two, one))
	if same, checked := bridge.checkTurnReports(); !same || !checked {
		t.Fatalf("same %v checked %v", same, checked)
	}

	// * 同じ番号では 1 回だけ。動ける妖怪を待つ間に同じログを繰り返さない
	if _, checked := bridge.checkTurnReports(); checked {
		t.Fatal("checked twice")
	}

	bridge.n = 6
	bridge.state[1].hp[0] = 99
	if same, checked := bridge.checkTurnReports(); same || !checked {
		t.Fatalf("desync same %v checked %v", same, checked)
	}
}

func TestFastUnitLapsSlowUnit(t *testing.T) {
	bridge := newVhostBridge()
	testSpeeds(bridge, []int{500, 1, 1, 1, 1, 1}, []int{1, 1, 1, 1, 1, 1})
	bridge.state[0].hp = readStateHP(testState(1, []uint16{9, 0, 0, 0, 0, 0}, []uint16{9, 9, 9, 9, 9, 9}))
	bridge.state[1].hp = readStateHP(testState(1, []uint16{9, 0, 0, 0, 0, 0}, []uint16{9, 0, 0, 0, 0, 0}))
	bridge.startClocks()
	bridge.wait[0][0], bridge.wait[1][0] = 10, 20

	fast := 0
	for n := 0; n < 40; n++ {
		side, position, ok := bridge.nextActor()
		if !ok {
			t.Fatal("nobody can act")
		}
		if side == 0 {
			fast++
		}
		bridge.finishTurn(side, position)
	}
	// * 待ち時間 3×31 と 3×123 なので、4 倍近く動く
	if fast < 30 {
		t.Fatalf("fast acted %d of 40", fast)
	}
}

func TestChargingUnitWaitStops(t *testing.T) {
	bridge := newVhostBridge()
	bridge.startClocks()
	bridge.wait[0] = [6]int{50, 200, 300, -1, -1, -1}
	bridge.wait[1] = [6]int{400, 400, 400, -1, -1, -1}
	bridge.state[0].flags = []uint32{0, 2, 0, 16, 16, 16}

	if side, position, ok := bridge.nextActor(); !ok || side != 0 || position != 0 {
		t.Fatalf("actor %d %d %v", side, position, ok)
	}
	if bridge.wait[0][1] != 200 || bridge.wait[0][2] != 250 {
		t.Fatalf("waits %v", bridge.wait[0])
	}
}

func TestBackRowKeepsItsWait(t *testing.T) {
	bridge := newVhostBridge()
	bridge.startClocks()
	bridge.wait[0] = [6]int{50, 60, 70, -1, -1, -1}
	bridge.wait[1] = [6]int{400, 400, 400, -1, -1, -1}

	// * 0 が下がり、3 がはじめて前に出る
	bridge.setFront(0, 1)
	if want := bridge.fullWait(0, 3); bridge.wait[0][3] != want {
		t.Fatalf("new unit %d, want %d", bridge.wait[0][3], want)
	}

	// * 下がっている間、0 の残りは減らない。戻るとそこから続く
	if side, position, ok := bridge.nextActor(); !ok || side != 0 || position != 1 {
		t.Fatalf("actor %d %d %v", side, position, ok)
	}
	if bridge.wait[0][0] != 50 || bridge.wait[0][2] != 10 {
		t.Fatalf("waits %v", bridge.wait[0])
	}
	bridge.setFront(0, 0)
	if bridge.wait[0][0] != 50 {
		t.Fatalf("returned with %d", bridge.wait[0][0])
	}
}

func TestSameWaitKeepsTheLine(t *testing.T) {
	bridge := newVhostBridge()
	bridge.startClocks()
	bridge.wait[0] = [6]int{400, 400, 400, -1, -1, -1}
	bridge.wait[1] = [6]int{400, 400, 400, -1, -1, -1}
	bridge.setWait(1, 2, 0)
	bridge.setWait(0, 1, 0)
	if side, position, ok := bridge.nextActor(); !ok || side != 1 || position != 2 {
		t.Fatalf("actor %d %d %v", side, position, ok)
	}
}

// * 上限の処理だけを通す。番は出さない（通信先がないため）
func (bridge *vhostBridge) tickLockedNoTurn() {
	bridge.nextAt = time.Now().Add(time.Hour)
	bridge.tickLocked(time.Now())
}

func testExecState(flags ...byte) []byte {
	data := make([]byte, bridgeStateSize)
	for n, flag := range flags {
		data[n*piaStateEntrySize+bridgeStateRow] = flag
	}
	return data
}

func TestExecHoldsTheNextTurn(t *testing.T) {
	bridge := newVhostBridge()
	operation := func(flags byte) bridgeOperation {
		data := make([]byte, bridgeOrderSize)
		data[bridgeOpFlags] = flags
		data[24], data[25] = 7, 2 // * side 1 の位置 1
		return bridgeOperation{side: 1, data: data}
	}

	// * 発動までを送り、残りは演出のあと
	bridge.queue = []bridgeOperation{operation(9), operation(bridgeOpExec), operation(1)}
	if taken := bridge.takeOperations(); len(taken) != 2 || len(bridge.queue) != 1 || !bridge.waitExec {
		t.Fatalf("taken %d left %d wait %v", len(taken), len(bridge.queue), bridge.waitExec)
	}

	// * 記録と同じ形: 演出の間の報告は溜めの印が残っている。本人は位置 1、相手は位置 7
	bridge.onExecReport(1, testExecState(0, 2))
	bridge.onExecReport(0, testExecState(0, 0, 0, 0, 0, 0, 0, 2))
	if bridge.execSeen[0] || bridge.execSeen[1] {
		t.Fatalf("seen during the special %v", bridge.execSeen)
	}

	bridge.onExecReport(1, testExecState(0, 0))
	if !bridge.waitExec {
		t.Fatal("moved on with one report")
	}
	bridge.onExecReport(0, testExecState())
	if bridge.waitExec || bridge.nextAt.IsZero() {
		t.Fatalf("still waiting %v", bridge.waitExec)
	}

	// * 発動がなければ止まらない
	bridge.queue = []bridgeOperation{operation(9), operation(1)}
	if taken := bridge.takeOperations(); len(taken) != 2 || bridge.waitExec {
		t.Fatalf("plain %d %v", len(taken), bridge.waitExec)
	}
}

func TestExecWaitHasALimit(t *testing.T) {
	bridge := newVhostBridge()
	bridge.running, bridge.waitExec = true, true
	bridge.waitedAt = time.Now().Add(-bridgeWaitExec)
	bridge.tickLockedNoTurn()
	if bridge.waitExec {
		t.Fatal("waits forever")
	}
}

func TestParseBridgePlayers(t *testing.T) {
	if players, ok := parseBridgePlayers("1700000001, 1700000002"); !ok || players != [2]uint32{1700000001, 1700000002} {
		t.Fatalf("%v %v", players, ok)
	}
	for _, bad := range []string{"", "1", "1,1", "a,b", "1,2,3", "0,1"} {
		if _, ok := parseBridgePlayers(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func testStateHash(n uint32, hash []byte, own []uint16, other []uint16) []byte {
	data := testState(n, own, other)
	copy(data[bridgeStateHash:], hash)
	return data
}

func testState(n uint32, own []uint16, other []uint16) []byte {
	data := make([]byte, bridgeStateSize)
	for i, hp := range append(append([]uint16(nil), own...), other...) {
		binary.LittleEndian.PutUint16(data[i*piaStateEntrySize+piaStateHP:], hp)
	}
	binary.LittleEndian.PutUint32(data[484:], n)
	return data
}

func TestVhostProbeTarget(t *testing.T) {
	bridge := newVhostBridge()
	bridge.sides[0] = &virtualHost{constID: vhostConstID}
	bridge.sides[1] = &virtualHost{constID: vhostConstID + 1}
	activeBridge, bridgePlayers = bridge, [2]uint32{11, 22}
	defer func() { activeBridge, bridgePlayers = nil, [2]uint32{} }()

	if host := vhostOfProbe(22, []string{"prudp:/address=1.2.3.4;port=60199;RVCID=101;sid=15"}); host != bridge.sides[1] {
		t.Fatal("own host not found")
	}
	// * 普通の対戦の接続先には反応しない
	if vhostOfProbe(22, []string{"prudp:/address=1.2.3.4;port=60100;RVCID=5;sid=15"}) != nil {
		t.Fatal("reacted to a real player")
	}
	if vhostOfProbe(22, []string{"prudp:/RVCID=1010"}) != nil {
		t.Fatal("matched a longer number")
	}
	if vhostOfProbe(33, []string{"prudp:/RVCID=100"}) != nil {
		t.Fatal("reacted to another player")
	}

	t.Setenv("YW2_DEV_VHOST_BOT", "1999999001")
	if vhostOfInvite(11, 1999999001) != bridge.sides[0] {
		t.Fatal("test event not used")
	}
	if vhostOfInvite(11, 1999999002) != nil {
		t.Fatal("cup used the test")
	}
}

func TestFirstTurnWithoutReport(t *testing.T) {
	bridge := newVhostBridge()
	if got := bridge.eligible(0); !sameInts(got, 0, 1, 2) {
		t.Fatalf("order %v", got)
	}
}

func TestReportsAndDefeat(t *testing.T) {
	one := []uint16{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	two := []uint16{7, 8, 9, 10, 11, 12, 1, 2, 3, 4, 5, 6}
	if same, _ := compareReports(one, two); !same {
		t.Fatal("same reports differ")
	}
	two[0] = 99
	if same, _ := compareReports(one, two); same {
		t.Fatal("different reports same")
	}

	bridge := newVhostBridge()
	if bridge.defeatedSide() != -1 {
		t.Fatal("defeated without reports")
	}
	bridge.state[0].hp = readStateHP(testState(1, []uint16{0, 0, 0, 5, 0, 0}, []uint16{9, 9, 9, 9, 9, 9}))
	bridge.state[1].hp = readStateHP(testState(1, []uint16{9, 9, 9, 9, 9, 9}, []uint16{0, 0, 0, 5, 0, 0}))
	if bridge.defeatedSide() != -1 {
		t.Fatal("one left is not defeated")
	}

	// * 前の 3 体が全滅なら後ろから
	if got := bridge.eligible(0); !sameInts(got, 3) {
		t.Fatalf("back %v", got)
	}
	if got := bridge.eligible(1); !sameInts(got, 0, 1, 2) {
		t.Fatalf("front %v", got)
	}

	bridge.state[0].hp = readStateHP(testState(1, []uint16{0, 0, 0, 0, 0, 0}, []uint16{9, 9, 9, 9, 9, 9}))
	if bridge.defeatedSide() != 0 {
		t.Fatal("not defeated")
	}
}

type vhostTestPeer struct {
	t      *testing.T
	socket *net.UDPConn
	host   *net.UDPAddr
	key    []byte
	seq    uint16
	acks   uint32
}

func (peer *vhostTestPeer) send(messages ...piaMessage) {
	peer.t.Helper()
	if _, err := peer.socket.WriteToUDP(buildPiaPacket(peer.key, 0x66, 1, 1, 0, messages...), peer.host); err != nil {
		peer.t.Fatal(err)
	}
}

// * 条件に合うメッセージが届くまで読む
func (peer *vhostTestPeer) wait(what string, match func(piaMessage) bool) piaMessage {
	peer.t.Helper()
	buffer := make([]byte, 2048)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		peer.socket.SetReadDeadline(deadline)
		size, _, err := peer.socket.ReadFromUDP(buffer)
		if err != nil {
			break
		}
		_, _, _, messages, ok := parsePiaPacket(buffer[:size])
		if !ok {
			peer.t.Fatalf("bad packet %x", buffer[:size])
		}
		for _, message := range messages {
			if match(message) {
				message.Body = append([]byte(nil), message.Body...)
				return message
			}
		}
	}
	peer.t.Fatalf("no %s", what)
	return piaMessage{}
}

func (peer *vhostTestPeer) waitGame(kind uint16) piaGame {
	peer.t.Helper()
	message := peer.wait("game message", func(message piaMessage) bool {
		game, ok := parsePiaGame(message.Body)
		return message.Proto == piaProtoGame && ok && game.Kind == kind
	})
	game, _ := parsePiaGame(message.Body)
	return game
}

func (peer *vhostTestPeer) game(kind uint16, data []byte) {
	peer.seq += 2
	peer.send(piaMessage{Dest: 1, Src: 2, Station: 1, Proto: piaProtoGame, Body: buildPiaGame(peer.seq, 1000, kind, data)})
}

func testHandshake(t *testing.T, peer *vhostTestPeer) {
	t.Helper()

	blocks := bytes.Repeat([]byte{0xAA}, 54)
	request := append([]byte{0x01, 0x66, 0x03, 0x00}, blocks...)
	request = append(request, 0xDE, 0xAD, 0xBE, 0xEF)
	peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoStation, Body: request})
	own := peer.wait("own request", func(message piaMessage) bool {
		return message.Proto == piaProtoStation && message.Body[0] == 0x01
	})
	peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoStation, Body: append([]byte{0x05, 0, 0, 0}, own.Body[58:]...)})

	response := append([]byte{0x02, 0x00, 0x03, 0x02}, make([]byte, 68)...)
	response = append(response, 1, 2, 3, 4)
	peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoStation, Body: response})
	ownResponse := peer.wait("own response", func(message piaMessage) bool {
		return message.Proto == piaProtoStation && message.Body[0] == 0x02
	})
	if len(ownResponse.Body) != 76 {
		t.Fatalf("own response %d", len(ownResponse.Body))
	}
	peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoStation, Body: append([]byte{0x05, 0, 0, 0}, ownResponse.Body[72:]...)})

	join := []byte{0x01, 0xFD, 0, 0, 1, 2, 3, 4, 5, 6, 0, 0, 9, 9, 9, 9}
	peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoMesh, Body: join})
	joined := peer.wait("join response", func(message piaMessage) bool {
		return message.Proto == piaProtoMesh && message.Port == 0
	})
	if len(joined.Body) != 124 || !bytes.Equal(joined.Body[64:118], blocks) {
		t.Fatalf("join response %d", len(joined.Body))
	}
	peer.send(piaMessage{Station: 0xFD, Src: 2, Proto: piaProtoStation, Body: append([]byte{0x05, 0, 0, 0}, joined.Body[120:]...)})

	peer.send(piaMessage{Station: 1, Src: 2, Proto: piaProtoClock, Body: make([]byte, 16)})
	mesh := peer.wait("mesh", func(message piaMessage) bool { return message.Proto == piaProtoMesh && message.Port == 1 })
	if len(mesh.Body) != 148 || binary.BigEndian.Uint32(mesh.Body[8:]) != 0xFFFFF82F {
		t.Fatalf("mesh %d", len(mesh.Body))
	}
	again := peer.wait("mesh again", func(message piaMessage) bool { return message.Proto == piaProtoMesh && message.Port == 1 })
	if binary.BigEndian.Uint32(again.Body[8:]) != 0xFFFFF82F {
		t.Fatalf("mesh counter moved %x", again.Body[8:12])
	}
	peer.send(piaMessage{Station: 1, Dest: 1, Src: 2, Proto: piaProtoMesh, Port: 1, Body: make([]byte, 24)})
}

func TestBridgeConnectsTwoPlayers(t *testing.T) {
	firstWait := bridgeFirstWait
	bridgeFirstWait = func(low int, high int) int { return low }
	defer func() { bridgeFirstWait = firstWait }()

	bridge := newVhostBridge()
	key := []byte("0123456789abcdef0123456789abcdef")
	var peers [2]*vhostTestPeer

	for side := range peers {
		port := relayTestFirstPort + 70 + side
		host, err := newVirtualHost("127.0.0.1", port, side, bridge)
		if err != nil {
			t.Fatal(err)
		}
		defer host.socket.Close()

		bridge.mutex.Lock()
		host.key = key
		host.bot = 1999999001
		bridge.mutex.Unlock()

		socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		defer socket.Close()
		socket.SetReadBuffer(1 << 20)
		peers[side] = &vhostTestPeer{t: t, socket: socket, host: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}, key: key}
	}
	go bridge.tick()

	// * 持ち主が先に確認を送る
	host := bridge.sides[0]
	host.startProbing([]*net.UDPAddr{peers[0].socket.LocalAddr().(*net.UDPAddr)})
	first := peers[0].wait("first probe", func(message piaMessage) bool { return message.Proto == piaProtoNAT && message.Port == 1 })
	if binary.BigEndian.Uint32(first.Body) != vhostConstID || first.Flags != piaFlagProbe {
		t.Fatalf("first probe %+v", first)
	}

	for _, peer := range peers {
		probe := append([]byte{0, 0, 0, 2, 0, 0, 0, 0}, 1, 2, 3, 4, 5, 6, 7, 8)
		peer.send(piaMessage{Flags: piaFlagProbe, Station: 0xFD, Src: 2, Proto: piaProtoNAT, Port: 1, Body: probe})
		reply := peer.wait("probe reply", func(message piaMessage) bool { return message.Proto == piaProtoNAT && message.Port == 2 })
		if !bytes.Equal(reply.Body[8:], probe[8:]) {
			t.Fatalf("probe reply %x", reply.Body)
		}
		testHandshake(t, peer)
	}

	// * 名前は相手の分が届く
	names := [][]byte{[]byte("name of zero"), []byte("name of one!")}
	for side, peer := range peers {
		peer.game(piaKindStart, make([]byte, 4))
		peer.game(piaKindPlayer, names[side])
	}
	for side, peer := range peers {
		if game := peer.waitGame(piaKindPlayer); !bytes.Equal(game.Data, names[1-side]) {
			t.Fatalf("name %q", game.Data)
		}
	}

	// * パーティの決定は、本人が決めたときだけ相手に届く。サーバーは先に送らない
	time.Sleep(500 * time.Millisecond)
	peers[0].game(piaKindReady, make([]byte, 4))
	peers[1].waitGame(piaKindReady)
	peers[1].game(piaKindReady, make([]byte, 4))
	peers[0].waitGame(piaKindReady)

	// * パーティは 2 人そろってから、相手の分が届く。種は同じ
	parties := [][]byte{[]byte("party of zero"), []byte("party of one!")}
	var seeds [2][]byte
	for side, peer := range peers {
		peer.game(piaKindParty, parties[side])
	}
	for side, peer := range peers {
		if game := peer.waitGame(piaKindParty); !bytes.Equal(game.Data, parties[1-side]) {
			t.Fatalf("party %q", game.Data)
		}
		seeds[side] = peer.waitGame(piaKindSeed).Data
	}
	if !bytes.Equal(seeds[0], seeds[1]) || len(seeds[0]) != 8 {
		t.Fatalf("seeds %x %x", seeds[0], seeds[1])
	}

	// * 開始の合図は 2 人そろってから
	peers[0].game(piaKindBattle, make([]byte, 4))
	time.Sleep(200 * time.Millisecond)
	bridge.mutex.Lock()
	early := bridge.sentStart
	bridge.mutex.Unlock()
	if early {
		t.Fatal("started with one player")
	}
	peers[1].game(piaKindBattle, make([]byte, 4))
	for _, peer := range peers {
		peer.waitGame(piaKindBattle)
	}

	// * 開始前の 4 つ。同じ妖怪に同じ乱数が当たる
	ask := func(hash byte) []byte {
		data := make([]byte, 88)
		data[8] = hash
		return data
	}
	for _, peer := range peers {
		peer.game(piaKindRequest, ask(0xA1))
		peer.game(piaKindRequest, ask(0xB2))
	}
	var starts [2][][]byte
	for side, peer := range peers {
		for n := 0; n < 4; n++ {
			starts[side] = append(starts[side], peer.waitGame(piaKindOrder).Data)
		}
		if starts[side][0][0] != 1 || starts[side][3][0] != 4 || starts[side][0][8] != 0xA1 || starts[side][2][8] != 0xB2 {
			t.Fatalf("start orders %x", starts[side])
		}
	}
	if !bytes.Equal(starts[0][0][64:68], starts[1][1][64:68]) || !bytes.Equal(starts[0][1][64:68], starts[1][0][64:68]) {
		t.Fatal("start randoms do not line up")
	}

	// * 最初の番。報告がそろうと、速い妖怪から番が来る
	full := []uint16{100, 100, 100, 100, 100, 100}
	for side, peer := range peers {
		first := testState(4, full, full)
		// * side 0 の先頭がいちばん速く、次が side 1 の先頭。自分の報告と相手の報告で位置が 6 ずれる
		binary.LittleEndian.PutUint16(first[(side*6)*piaStateEntrySize+bridgeStateSpeed:], 500)
		binary.LittleEndian.PutUint16(first[((1-side)*6)*piaStateEntrySize+bridgeStateSpeed:], 400)
		peer.game(piaKindState, first)
	}
	turn := peers[0].waitGame(piaKindTurn)
	if turn.Data[4] != 8 || turn.Data[32] != 6 || turn.Data[33] != 2 {
		t.Fatalf("turn %x", turn.Data)
	}

	// * side 0 の行動が、相手には鏡の位置で届き、乱数は同じ
	action := make([]byte, 96)
	action[4] = 2
	copy(action[8:], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	action[32], action[33], action[34], action[35] = 6, 2, 1, 1
	peers[0].game(piaKindAction, action)

	own := peers[0].waitGame(piaKindOrder)
	seen := peers[1].waitGame(piaKindTurn)
	other := peers[1].waitGame(piaKindOrder)
	if own.Data[0] != 5 || other.Data[0] != 5 || !bytes.Equal(own.Data[8:12], []byte{0xDE, 0xAD, 0xBE, 0xEF}) || !bytes.Equal(other.Data[8:12], own.Data[8:12]) {
		t.Fatalf("orders %x %x", own.Data, other.Data)
	}
	if !bytes.Equal(own.Data[24:28], []byte{6, 2, 1, 1}) || !bytes.Equal(other.Data[24:28], []byte{0, 1, 7, 2}) || seen.Data[32] != 0 || seen.Data[33] != 1 {
		t.Fatalf("slots %x %x %x", own.Data[24:28], other.Data[24:28], seen.Data[32:34])
	}
	if !bytes.Equal(own.Data[64:68], other.Data[64:68]) || own.Data[86] != 1 || other.Data[86] != 0 {
		t.Fatalf("random or owner %x %x", own.Data[64:88], other.Data[64:88])
	}

	// * 次は相手の番
	damaged := []uint16{100, 50, 100, 100, 100, 100}
	dead := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	// * 片方だけの報告では、次の番は出ない
	peers[0].game(piaKindState, testStateHash(5, dead, full, damaged))
	time.Sleep(600 * time.Millisecond)
	bridge.mutex.Lock()
	waiting := bridge.waitState
	bridge.mutex.Unlock()
	if !waiting {
		t.Fatal("moved on with one report")
	}
	peers[1].game(piaKindState, testStateHash(5, dead, damaged, full))
	next := peers[1].waitGame(piaKindTurn)
	if next.Data[32] != 6 || next.Data[33] != 2 {
		t.Fatalf("next turn %x", next.Data)
	}

	// * 操作は今の行動が終わるまで確定しない。次の番の直前に 2 人に届き、前の 3 体が変わる
	rotate := make([]byte, bridgeOrderSize)
	copy(rotate[8:], []byte{0xB0, 0xAA, 0xF0, 0x88})
	rotate[26], rotate[27], rotate[72] = 9, 2, 3
	peers[1].game(piaKindRequest, rotate)
	time.Sleep(300 * time.Millisecond)
	bridge.mutex.Lock()
	queued := len(bridge.queue)
	bridge.mutex.Unlock()
	if queued != 1 {
		t.Fatalf("queued %d", queued)
	}

	second := make([]byte, 96)
	second[4] = 2
	copy(second[8:], []byte{0x11, 0x22, 0x33, 0x44})
	second[32], second[33], second[34], second[35] = 6, 2, 1, 1
	peers[1].game(piaKindAction, second)
	if game := peers[1].waitGame(piaKindOrder); !bytes.Equal(game.Data[8:12], second[8:12]) {
		t.Fatalf("second action %x", game.Data)
	}
	if game := peers[0].waitGame(piaKindOrder); !bytes.Equal(game.Data[8:12], second[8:12]) {
		t.Fatalf("second action seen %x", game.Data)
	}
	peers[0].game(piaKindState, testStateHash(6, second[8:12], full, damaged))
	peers[1].game(piaKindState, testStateHash(6, second[8:12], damaged, full))

	mine := peers[1].waitGame(piaKindOrder)
	theirs := peers[0].waitGame(piaKindOrder)
	if mine.Data[0] != 7 || theirs.Data[0] != 7 || mine.Data[26] != 9 || theirs.Data[26] != 3 || theirs.Data[27] != 1 {
		t.Fatalf("operation %x %x", mine.Data, theirs.Data)
	}
	bridge.mutex.Lock()
	front := bridge.front[1]
	bridge.mutex.Unlock()
	if front != 3 { // * 値 3 で回したので、前は 3,4,5
		t.Fatalf("front %d", front)
	}

	// * 開始前の知らせは中身ごと相手へ渡し、前の 3 体も変わる
	notice := make([]byte, bridgeTurnSize)
	notice[4] = bridgeTurnStart
	notice[bridgeStartFront] = 2
	notice[90] = 4
	peers[0].game(piaKindAction, notice)
	if game := peers[1].waitGame(piaKindTurn); game.Data[4] != bridgeTurnStart || game.Data[bridgeStartFront] != 2 || game.Data[90] != 0 {
		t.Fatalf("notice %x", game.Data)
	}
	bridge.mutex.Lock()
	front = bridge.front[0]
	bridge.mutex.Unlock()
	if front != 2 {
		t.Fatalf("start front %d", front)
	}
}

// * 記録（2026-11-07 01:08）: 参加者の回転の始まりは、同じ側の終わりが届くまで確定しない。間の番は進む
func TestRotateBeginWaitsForEnd(t *testing.T) {
	bridge := newVhostBridge()
	operation := func(side int, hash uint32) bridgeOperation {
		data := make([]byte, bridgeOrderSize)
		binary.BigEndian.PutUint32(data[8:12], hash)
		data[bridgeOpFlags] = 9
		return bridgeOperation{side: side, data: data}
	}

	// * 相手の操作は先に送る。始まりから後ろは残す
	bridge.queue = []bridgeOperation{operation(1, 0x8899cf23), operation(0, bridgeRotateBegin), operation(1, 0xa96e9d8f)}
	if taken := bridge.takeOperations(); len(taken) != 1 || len(bridge.queue) != 2 {
		t.Fatalf("taken %d left %d", len(taken), len(bridge.queue))
	}

	// * 相手の回転の終わりでは確定しない
	bridge.queue = append(bridge.queue, operation(1, bridgeRotateMon))
	if taken := bridge.takeOperations(); len(taken) != 0 {
		t.Fatalf("taken %d with the other side's end", len(taken))
	}

	// * 同じ側の終わりが届いたら、まとめて送る
	bridge.queue = append(bridge.queue, operation(0, bridgeRotateMon))
	if taken := bridge.takeOperations(); len(taken) != 4 || len(bridge.queue) != 0 {
		t.Fatalf("taken %d left %d", len(taken), len(bridge.queue))
	}
}

// * 報告の中身の比べ方: side0 の本人の側は side1 の相手の側と並べる
func TestStateEntryDiffs(t *testing.T) {
	one := make([]byte, bridgeStateSize)
	two := make([]byte, bridgeStateSize)
	if lines := stateEntryDiffs(one, two); len(lines) != 0 {
		t.Fatalf("same reports %v", lines)
	}
	one[2*piaStateEntrySize+28] = 1 // * side0 の位置 2 の状態
	two[8*piaStateEntrySize+28] = 1 // * side1 から見た同じ妖怪
	if lines := stateEntryDiffs(one, two); len(lines) != 0 {
		t.Fatalf("mirrored entries %v", lines)
	}
	two[0*piaStateEntrySize+8] = 9 // * side1 の位置 0 の力（side0 から見て 6 番目）
	lines := stateEntryDiffs(one, two)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "side0[6] ") {
		t.Fatalf("diff %v", lines)
	}
}

// * 必殺技の狙い: 挑発 → ねらう → おんみつ以外。自分を狙う技と、狙いが入っている要求は触らない
func TestFillSpecialTarget(t *testing.T) {
	bridge := newVhostBridge()
	bridge.specials = map[string][2]int{"272b3181": {1, 2}, "aaaaaaaa": {2, 1}, "4c5ea107": {5, 5}}
	for side := range bridge.state {
		bridge.state[side].hp = []uint16{100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100}
		bridge.state[side].flags = make([]uint32, bridgeTeamMon)
		bridge.state[side].status = make([]uint32, bridgeTeamMon)
	}
	request := func(hash string) []byte {
		data := make([]byte, bridgeOrderSize)
		raw, _ := hex.DecodeString(hash)
		copy(data[8:12], raw)
		data[24], data[25] = 6, 2
		data[bridgeOpFlags] = bridgeOpExec
		return data
	}

	// * side1 の位置 1 が挑発されている。side0 の必殺技はそこを狙う（後ろの列の位置 4 は狙わない）
	bridge.state[1].status[1] = bridgeStatusTaunt
	bridge.state[1].status[4] = bridgeStatusTaunt
	for n := 0; n < 20; n++ {
		data := request("272b3181")
		bridge.fillSpecialTarget(0, data)
		if data[26] != 1 || data[27] != 1 {
			t.Fatalf("taunt target %d/%d", data[26], data[27])
		}
	}

	// * 挑発がなければ「ねらう」の相手。おんみつなら、ねらうでも狙わない
	bridge.state[1].status[1] = 0
	bridge.noteAim(0, append(make([]byte, bridgeActionAim), 2, 1), bridgeActionAim)
	data := request("272b3181")
	bridge.fillSpecialTarget(0, data)
	if data[26] != 2 || data[27] != 1 {
		t.Fatalf("aim target %d/%d", data[26], data[27])
	}
	bridge.state[1].status[2] = bridgeStatusStealth
	bridge.state[1].hp[0] = 0
	for n := 0; n < 20; n++ {
		data := request("272b3181")
		bridge.fillSpecialTarget(0, data)
		if data[26] != 1 || data[27] != 1 {
			t.Fatalf("open target %d/%d", data[26], data[27])
		}
	}

	// * 味方を狙う技は自分の前の 3 体から。自分や両方を狙う技、狙いの入った要求はそのまま
	data = request("aaaaaaaa")
	bridge.fillSpecialTarget(1, data)
	if data[27] != 2 || data[26] < 6 || data[26] > 8 {
		t.Fatalf("ally target %d/%d", data[26], data[27])
	}
	data = request("4c5ea107")
	bridge.fillSpecialTarget(0, data)
	if data[27] != 0 {
		t.Fatal("filled a whole-field special")
	}
	data = request("272b3181")
	data[26], data[27] = 0, 1
	bridge.fillSpecialTarget(0, data)
	if data[26] != 0 {
		t.Fatal("changed a given target")
	}
}

// * 操作を確定したあとは、2 人の報告が届くまで次の番を出さない。片方だけでは待つ
func TestOperationsWaitForReports(t *testing.T) {
	bridge := newVhostBridge()
	bridge.running = true
	bridge.waitOps = true
	bridge.opsBase = [2]int{bridge.state[0].count, bridge.state[1].count}
	bridge.waitedAt = time.Now()
	report := make([]byte, bridgeStateSize)
	bridge.onState(0, report)
	if !bridge.waitOps {
		t.Fatal("moved on with one report")
	}
	bridge.onState(1, report)
	if bridge.waitOps || bridge.nextAt.IsZero() {
		t.Fatal("still waiting after both reports")
	}

	// * 報告が来なくても、上限を過ぎれば進める
	bridge.waitOps = true
	bridge.waitedAt = time.Now().Add(-bridgeWaitOps)
	bridge.nextAt = time.Time{}
	bridge.tickLocked(time.Now())
	if bridge.waitOps {
		t.Fatal("waited past the limit")
	}
}

// * 試験の「持ち主が落ちた」状態: 部屋の URL をサーバーの局として返さなくなる
func TestSilentHostIsNotTheRoom(t *testing.T) {
	bridge := newVhostBridge()
	host := &virtualHost{mutex: &bridge.mutex, bridge: bridge, key: []byte{1}, gid: 77}
	if !host.isRoom(77) {
		t.Fatal("room not found")
	}
	host.silent = true
	if host.isRoom(77) {
		t.Fatal("silent host still answers for the room")
	}
}
