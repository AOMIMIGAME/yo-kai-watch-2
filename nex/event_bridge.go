package nex

// 試験用: 別々の部屋に入った 2 人をつなぐ。サーバーが 2 人の持ち主として、順番と乱数を決める。
//
// 読み取った対戦の流れ（44_対戦の接続手順の解読.md）
//   持ち主 → 参加者 0x1003「この妖怪の番」
//   参加者 → 持ち主 0x1002「技と相手」
//   持ち主 → 参加者 0x1005「確定」（技、行動する妖怪、相手、乱数）
//   参加者 → 持ち主 0x1008「全員の HP と、受けた番号」
//
// 妖怪の位置: 参加者の側は 6〜11（チーム 2）、持ち主の側は 0〜5（チーム 1）。
// 番の決め方はゲームのコードと同じ形（FUN_00555634 ほか、44_対戦の接続手順の解読.md）。
// 妖怪ごとの残りの値が最も少ない妖怪が動き、動いた分を全員から引く。実時間は使わない。
// 素早さは持ち物込みの報告の値に、特性、魂、状態の効果を足す（currentSpeed）。

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

const (
	bridgeOrderSize       = 88
	bridgeTurnSize        = 96
	bridgeStateSize       = 504
	bridgeTeamMon         = 6
	bridgeTurnGap         = 300 * time.Millisecond
	bridgeWaitAction      = 90 * time.Second // * 必殺技の入力中は本体が番の返事を止める。番を飛ばすと 2 台とも止まる
	bridgeWaitState       = 8 * time.Second
	bridgeTurnEnd         = 0x22
	bridgeTurnStart       = 0x0E       // * 開始前の画面が終わった知らせと見ている。参加者は 0x1002、持ち主は 0x1003 で送る
	bridgeRotateMon       = 0xb0aaf088 // * 回転で前へ出す妖怪。リトルエンディアンで読まない、記録のままの並び
	bridgeRotateBegin     = 0x269af7ff // * 回転の始まり。本物は同じ側の 0xb0aaf088 が届くまで確定しない
	bridgeOpValue         = 72
	bridgeOpFlags         = 84
	bridgeStartFront      = 76
	bridgeStateCharge     = 0x02 // * 報告の印: 溜めている。本物の持ち主はこの妖怪に番を回さない
	bridgeSlotCount       = 12   // * 12 以上は妖怪以外（つつくの指など）。番号はそのまま
	bridgeStateHash       = 492  // * 報告の中の、最後に受けた確定の技の値
	bridgeStateRow        = 24   // * 報告の 1 体分の中の印
	bridgeStateSpeed      = 14   // * 報告の 1 体分の中の素早さ（装備を含む値と見ている）
	bridgeDefaultSpeed    = 100  // * 報告が来なかったときの素早さ
	bridgeWaitSpeed       = 2 * time.Second
	bridgeWaitExec        = 25 * time.Second // * 必殺技の演出を待つ上限
	bridgeWaitOps         = 5 * time.Second  // * 操作の確定のあと、報告を待つ上限
	bridgeOpExec          = 0x81             // * 操作の [84]: 必殺技や G 技の発動
	bridgeWaitRows        = 94
	bridgePartyRecord     = 80
	bridgePartyItem       = 24 // * 持ち物 2 つ。魂もここに入る
	bridgePartySpecies    = 44
	bridgePartyHouse      = 76 // * 11 ビット目から 3 ビット。1 が元祖、2 が本家と見ている
	bridgeHouseMain       = 2
	bridgeSeedTries       = 20000000
	bridgeCannotAct       = 0x70C2 // * FUN_00657a38 の 0x70D3 から、倒れている 0x01 と後列 0x10 を除く
	bridgeCannotActMiddle = 0x7042
	bridgeCannotActStrong = 0x7040
	bridgeExtraMiddle     = 0x4000
	bridgeExtraStrong     = 0x10000
	bridgeStatusBad       = 0x0F00000F
	bridgeStatusSpeedUp   = 0x40000
	bridgeStatusSpeedDown = 0x4000000
	bridgeStatusStage     = 2 // * 状態 1 つ分の段。実機で未確認
	bridgeStageCount      = 17
	bridgeStageMax        = 8
	bridgePartySoulLevel  = 58
	bridgeWaitSteps       = 3
	bridgeNoWait          = -1   // * まだ待ち時間を持っていない（後列のまま）
	bridgeStateBack       = 0x10 // * 後列。0x01 は倒れている
)

type bridgeSide struct {
	player     []byte
	party      []byte
	battle     bool
	asks       [][]byte
	hp         []uint16 // * 本人の報告。本人の側 6 体、相手の側 6 体
	lastN      uint32
	count      int // * 届いた報告の数
	lastHash   []byte
	flags      []uint32     // * 本人の報告の、本人の側 6 体の印（妖怪の +0x3C8）
	status     []uint32     // * 同じく +0x3CC。状態
	extra      []uint32     // * 同じく +0x380
	plainSpeed []int        // * 持ち物なしの素早さ
	speed      []int        // * 最初の報告の素早さ。本人の側 6 体、相手の側 6 体
	units      []bridgeUnit // * パーティから読んだ素早さの効果
	prepare    bool
}

type bridgeUnit struct {
	effect  speedEffect // * 本人の特性
	item    speedEffect // * 魂と持ち物
	house   int
	tribe   int
	present bool
}

type bridgeOperation struct {
	side int
	data []byte
}

type vhostBridge struct {
	mutex sync.Mutex
	sides [2]*virtualHost
	state [2]bridgeSide

	sentNames bool
	sentParty bool
	sentStart bool
	sentAsks  bool
	seed      []byte

	running        bool
	ended          bool
	n              uint32
	actingSide     int
	actingPosition int
	front          [2]int                // * 前の 3 体の先頭の位置。回転で変わる
	wait           [2][bridgeTeamMon]int // * 動くまでの残り
	waitOrder      [2][bridgeTeamMon]int // * 並んだ順。残りが同じなら先に並んだほう
	goesFirst      [2][bridgeTeamMon]bool
	stages         []int
	tribeBonus     [][3]int
	resetSkills    map[string]bool
	actingSkill    string
	queued         int
	clocks         bool
	startedAt      time.Time
	queue          []bridgeOperation
	waitHash       []byte // * 報告が返すのを待つ技の値
	waitN          uint32 // * 報告を待つ行動の番号。操作の確定には報告を待たない
	checkedN       uint32 // * 番の直前の比較を済ませた通し番号
	waitAction     bool
	waitExec       bool   // * 発動の演出が終わるのを待っている
	waitOps        bool   // * 操作の確定のあと、2 人の報告を待っている（本物も確定 → 報告 → 番の順）
	opsBase        [2]int // * 確定したときの報告の数
	execSeen       [2]bool
	execActor      int // * 発動した妖怪の位置（持ち主の側）
	execSide       int
	waitState      bool
	waitedAt       time.Time
	nextAt         time.Time
	reports        map[uint32][2][]uint16
	rawReports     map[uint32][2][]byte // * 照合用: 報告の中身そのもの。HP 以外（能力・状態・印）のずれも見る
	specials       map[string][2]int    // * 必殺技の狙いの種類（history-tables の specials）
	aim            [2][2]byte           // * それぞれの「ねらう」の相手。要求の位置（参加者の見方、番号とチーム）
}

var activeBridge *vhostBridge

// * 開始時の待ち時間の乱数。low 以上 high 以下。試験で差し替える
var bridgeFirstWait = func(low int, high int) int {
	return low + int(binary.LittleEndian.Uint32(randomBytes(4))%uint32(high-low+1))
}
var bridgePlayers [2]uint32

func parseBridgePlayers(text string) ([2]uint32, bool) {
	parts := strings.Split(text, ",")
	if len(parts) != 2 {
		return [2]uint32{}, false
	}

	var players [2]uint32
	for n, part := range parts {
		value, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		if err != nil || value == 0 {
			return [2]uint32{}, false
		}
		players[n] = uint32(value)
	}
	return players, players[0] != players[1]
}

func newVhostBridge() *vhostBridge {
	bridge := &vhostBridge{}
	bridge.resetLocked()
	return bridge
}

func startVirtualHost(relay *packetRelay) {
	players, ok := parseBridgePlayers(os.Getenv("YW2_DEV_VHOST_PID"))
	if !ok {
		return
	}

	bridge := newVhostBridge()
	for side := range bridge.sides {
		port := vhostPort + side
		relay.reservePort(port)
		if _, err := newVirtualHost(relay.publicIP, port, side, bridge); err != nil {
			fmt.Printf("[YW2-VHOST] not started: %v\n", err)
			return
		}
	}
	bridgePlayers = players
	activeBridge = bridge
	go bridge.tick()
	fmt.Printf("[YW2-VHOST] started for %v\n", players)
}

func vhostOfPlayer(pid uint32) *virtualHost {
	if activeBridge == nil {
		return nil
	}
	for side, player := range bridgePlayers {
		if player == pid {
			return activeBridge.sides[side]
		}
	}
	return nil
}

// * 実験に使うイベント。ほかのイベントは普通の組み合わせのまま
func vhostOfInvite(player uint32, bot uint32) *virtualHost {
	if strconv.FormatUint(uint64(bot), 10) != os.Getenv("YW2_DEV_VHOST_BOT") {
		return nil
	}
	return vhostOfPlayer(player)
}

// * 接続先がサーバーの演じる持ち主か
func vhostOfProbe(pid uint32, targets []string) *virtualHost {
	host := vhostOfPlayer(pid)
	if host == nil {
		return nil
	}
	mark := "RVCID=" + strconv.FormatUint(uint64(host.constID), 10)
	for _, target := range targets {
		for _, part := range strings.Split(strings.TrimPrefix(target, "prudp:/"), ";") {
			if part == mark {
				return host
			}
		}
	}
	return nil
}

func vhostOfRoom(gid uint32) *virtualHost {
	if activeBridge == nil {
		return nil
	}
	for _, host := range activeBridge.sides {
		if host != nil && host.isRoom(gid) {
			return host
		}
	}
	return nil
}

func (bridge *vhostBridge) resetLocked() {
	bridge.state = [2]bridgeSide{}
	bridge.sentNames = false
	bridge.sentParty = false
	bridge.sentStart = false
	bridge.sentAsks = false
	bridge.running = false
	bridge.ended = false
	bridge.n = 0
	bridge.actingPosition = 0
	bridge.front = [2]int{}
	bridge.aim = [2][2]byte{}
	bridge.wait = [2][bridgeTeamMon]int{}
	bridge.goesFirst = [2][bridgeTeamMon]bool{}
	bridge.waitOrder = [2][bridgeTeamMon]int{}
	bridge.queued = 0
	bridge.clocks = false
	bridge.waitN = 0
	bridge.queue = nil
	bridge.waitHash = nil
	bridge.waitAction = false
	bridge.waitState = false
	bridge.waitExec = false
	bridge.waitOps = false
	bridge.checkedN = 0
	bridge.nextAt = time.Time{}
	bridge.reports = make(map[uint32][2][]uint16)
	bridge.rawReports = make(map[uint32][2][]byte)

	bridge.seed = make([]byte, 8)
	rand.Read(bridge.seed[:4])
	bridge.seed[4] = 1
}

// * 前の対戦が残っていたら、新しく始める
func (bridge *vhostBridge) onPrepared(side int) {
	if bridge.state[side].prepare || bridge.running || bridge.ended {
		bridge.resetLocked()
	}
	bridge.state[side].prepare = true
}

func (bridge *vhostBridge) waitPrepared(limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		bridge.mutex.Lock()
		ready := bridge.state[0].prepare && bridge.state[1].prepare
		bridge.mutex.Unlock()
		if ready {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (bridge *vhostBridge) onLeft(side int) {
	if bridge.running {
		bridge.running = false
		bridge.ended = true
		fmt.Printf("[YW2-VHOST] side %d left, battle over\n", side)
	}
}

// * 位置の 2 バイト（番号、チーム）を、相手の本体での見え方に直す
func mirrorSlot(index byte, team byte) (byte, byte) {
	if team == 2 && index >= bridgeTeamMon {
		return index - bridgeTeamMon, 1
	}
	if team == 1 && index < bridgeTeamMon {
		return index + bridgeTeamMon, 2
	}
	return index, team
}

// * 持ち主が使う妖怪の番号（2 組の u16）。参加者の側は記録の規則のまま。持ち主の側は本体の値で、位置から作る
func slotHandle(index byte, team byte) []byte {
	var first, second uint16
	if team == 2 {
		first = 0x1002 + uint16(index) - bridgeTeamMon
		second = 3 + uint16(index) - bridgeTeamMon
	} else {
		first = 0x80 + uint16(index)
		second = 0x82 + uint16(index)
	}
	out := binary.LittleEndian.AppendUint16(nil, first)
	return binary.LittleEndian.AppendUint16(out, second)
}

func buildTurn(index byte, team byte) []byte {
	data := make([]byte, bridgeTurnSize)
	data[4] = 8
	copy(data[24:], slotHandle(index, team))
	data[32] = index
	data[33] = team
	return data
}

func buildTurnEnd() []byte {
	data := make([]byte, bridgeTurnSize)
	data[4] = bridgeTurnEnd
	return data
}

func buildOrder(n uint32, hash []byte, actorIndex byte, actorTeam byte, targetIndex byte, targetTeam byte, random []byte) []byte {
	data := make([]byte, bridgeOrderSize)
	binary.LittleEndian.PutUint32(data, n)
	copy(data[8:], hash)
	copy(data[16:], slotHandle(actorIndex, actorTeam))
	copy(data[20:], slotHandle(targetIndex, targetTeam))
	data[24] = actorIndex
	data[25] = actorTeam
	data[26] = targetIndex
	data[27] = targetTeam
	copy(data[64:], random)
	data[84] = 0x60
	if actorTeam == 2 {
		data[86] = 1 // * 参加者の妖怪の行動
	}
	return data
}

// * 行動の要求の付加情報を確定へ移す。[52] の印は [32] へ。
// * ピンは要求の [68:74]（本人の数え方の番号と位置）から、確定の [56:62]（持ち主の数え方）へ
func copyActionDetails(own []byte, seen []byte, request []byte) {
	if len(request) < 74 {
		return
	}

	own[32] = request[52]
	seen[32] = request[52]

	pinIndex, pinTeam := request[72], request[73]
	if pinTeam == 0 {
		return
	}
	copy(own[56:], slotHandle(pinIndex, pinTeam))
	own[60], own[61] = pinIndex, pinTeam

	mirrorIndex, mirrorTeam := mirrorSlot(pinIndex, pinTeam)
	copy(seen[56:], slotHandle(mirrorIndex, mirrorTeam))
	seen[60], seen[61] = mirrorIndex, mirrorTeam
}

func buildStartOrder(n uint32, hash []byte, joinerTurn bool, random []byte) []byte {
	data := make([]byte, bridgeOrderSize)
	binary.LittleEndian.PutUint32(data, n)
	copy(data[8:], hash)
	copy(data[64:], random)
	data[84] = 1
	if joinerTurn {
		data[86] = 1
	}
	return data
}

func readStateHP(data []byte) []uint16 {
	hp := make([]uint16, 0, 12)
	for n := 0; n < 12; n++ {
		hp = append(hp, binary.LittleEndian.Uint16(data[n*piaStateEntrySize+piaStateHP:]))
	}
	return hp
}

func stateOrder(data []byte) uint32 {
	return binary.LittleEndian.Uint32(data[484:])
}

func randomBytes(size int) []byte {
	out := make([]byte, size)
	rand.Read(out)
	return out
}

func (bridge *vhostBridge) onGame(side int, kind uint16, data []byte) {
	other := 1 - side
	state := &bridge.state[side]

	switch kind {
	case piaKindPlayer:
		state.player = append([]byte(nil), data...)
		if bridge.state[other].player != nil && !bridge.sentNames && os.Getenv("YW2_DEV_VHOST_MIGRATE") == "true" {
			bridge.sentNames = true
			bridge.startMigrationProbe(0)
			break
		}
		if bridge.state[other].player != nil && !bridge.sentNames {
			bridge.sentNames = true
			fmt.Printf("[YW2-VHOST] both names\n")
			for n, host := range bridge.sides {
				host.sendGameLocked(piaKindHello, []byte{2, 0, 0, 0})
				host.sendGameLocked(piaKindPlayer, bridge.state[1-n].player)
				host.sendGameLocked(piaKindJoined, make([]byte, 4))
			}
		}
	case piaKindReady:
		// * パーティの決定。本物は 2 人とも送ったあと、8 秒ほどで 2 人がパーティを送る。
		// * サーバーが先に送ると、相手が決める前に本人だけ先へ進んでしまう
		fmt.Printf("[YW2-VHOST] side %d decided the party\n", side)
		bridge.sides[other].sendGameLocked(piaKindReady, make([]byte, 4))
	case piaKindParty:
		state.party = append([]byte(nil), data...)
		if history != nil {
			bridge.readParty(side, data, history.tables)
		}
		if bridge.state[other].party != nil && !bridge.sentParty {
			bridge.sentParty = true
			bridge.prepareShuffle()
			fmt.Printf("[YW2-VHOST] both parties\n")
			for n, host := range bridge.sides {
				host.sendGameLocked(piaKindParty, bridge.state[1-n].party)
				host.sendGameLocked(piaKindSeed, bridge.seed)
			}
		}
	case piaKindBattle:
		// * 2 人そろってから開始の合図を渡すので、ルーレットが同時に回る
		state.battle = true
		if bridge.state[other].battle && !bridge.sentStart {
			bridge.sentStart = true
			fmt.Printf("[YW2-VHOST] both ready to start\n")
			for _, host := range bridge.sides {
				host.sendGameLocked(piaKindBattle, make([]byte, 4))
			}
		}
	case piaKindRequest:
		if len(data) >= 12 && len(state.asks) < 2 {
			state.asks = append(state.asks, append([]byte(nil), data[8:12]...))
		}
		if len(state.asks) == 2 && len(bridge.state[other].asks) == 2 && !bridge.sentAsks {
			bridge.sentAsks = true
			bridge.sendStartOrders()
		} else if bridge.running {
			bridge.onOperation(side, data)
		}
	case piaKindAction:
		// * 開始前の回転の結果。相手には持ち主の形で渡す
		if len(data) > 4 && data[4] == bridgeTurnStart {
			bridge.setFront(side, startFront(data))
			fmt.Printf("[YW2-VHOST] side %d starts with front %d\n", side, bridge.front[side])
			bridge.sides[other].sendGameLocked(piaKindTurn, buildTurnStart(bridge.front[side]))
			return
		}
		bridge.onAction(side, data)
	case piaKindState:
		bridge.onState(side, data)
	case piaKindRematch, piaKindDecline:
		// * 連戦はしない
		bridge.sides[side].sendGameLocked(piaKindDecline, make([]byte, 4))
		bridge.sides[other].sendGameLocked(piaKindDecline, make([]byte, 4))
	}
}

// * 開始前の 4 つ。2 人の本体で同じ妖怪に同じ乱数が当たるように、順番に合わせて入れ替える
func (bridge *vhostBridge) sendStartOrders() {
	hashes := bridge.state[0].asks
	randoms := [][]byte{randomBytes(4), randomBytes(4), randomBytes(4), randomBytes(4)}
	for side, host := range bridge.sides {
		for n := 0; n < 4; n++ {
			joinerTurn := n%2 == 1
			// * 本人の妖怪の分は side の乱数、相手の妖怪の分はもう一方の乱数
			owner := side
			if !joinerTurn {
				owner = 1 - side
			}
			random := randoms[n/2*2+owner]
			host.sendGameLocked(piaKindOrder, buildStartOrder(uint32(n+1), hashes[n/2], joinerTurn, random))
			fmt.Printf("[YW2-VHOST] start n=%d side %d random %x\n", n+1, side, random)
		}
	}
	bridge.n = 4
	bridge.running = true
	bridge.startedAt = time.Now()
	// * 本物はこのあとすぐ最初の番を出す。この 4 つには報告の番号が返らないので、待たない
	bridge.nextAt = time.Now().Add(bridgeTurnGap)
	fmt.Printf("[YW2-VHOST] BATTLE RUNNING\n")
}

// * 回転、おはらいなどの操作。本物の持ち主と同じように、通し番号と乱数を付けて 2 人に配る
func (bridge *vhostBridge) onOperation(side int, data []byte) {
	if len(data) != bridgeOrderSize {
		return
	}
	bridge.noteAim(side, data, bridgeOrderAim)

	// * 本物は要求をすぐ確定せず、今の行動が終わって次の番を出す直前にまとめて確定する
	bridge.queue = append(bridge.queue, bridgeOperation{side: side, data: append([]byte(nil), data...)})
}

// * 今送る操作を取り出す。発動を含むなら、そこで止める。残りは演出が終わってから
func (bridge *vhostBridge) takeOperations() []bridgeOperation {
	for n, operation := range bridge.queue {
		// * 回転の始まりだけ確定すると、2 台とも終わりを待って止まる。終わりが届くまで、ここから後ろは残す
		if binary.BigEndian.Uint32(operation.data[8:12]) == bridgeRotateBegin && !bridge.rotateEndQueued(operation.side, n+1) {
			taken := bridge.queue[:n]
			bridge.queue = bridge.queue[n:]
			return taken
		}
		if operation.data[bridgeOpFlags] != bridgeOpExec {
			continue
		}
		taken := bridge.queue[:n+1]
		bridge.queue = bridge.queue[n+1:]
		bridge.waitExec = true
		bridge.execSeen = [2]bool{}
		bridge.execSide = operation.side
		bridge.execActor = int(operation.data[24]) - bridgeTeamMon
		bridge.waitedAt = time.Now()
		return taken
	}
	taken := bridge.queue
	bridge.queue = nil
	return taken
}

// * from より後ろに、その側の回転の終わり（0xb0aaf088）が届いているか
func (bridge *vhostBridge) rotateEndQueued(side int, from int) bool {
	for _, operation := range bridge.queue[from:] {
		if operation.side == side && binary.BigEndian.Uint32(operation.data[8:12]) == bridgeRotateMon {
			return true
		}
	}
	return false
}

// * 記録では、本物の持ち主は発動を確定したあと、演出が終わった報告が来るまで次の番を出さない。
// * その報告の技の値と番号は発動の前と同じなので、溜めの印（0x02）が消えたことで見分ける
func (bridge *vhostBridge) onExecReport(side int, data []byte) {
	if !bridge.waitExec {
		return
	}
	entry := bridge.execActor
	if side != bridge.execSide {
		entry += bridgeTeamMon
	}
	if entry >= 0 && entry < 12 && data[entry*piaStateEntrySize+bridgeStateRow]&bridgeStateCharge != 0 {
		return
	}
	bridge.execSeen[side] = true
	if bridge.execSeen[0] && bridge.execSeen[1] {
		bridge.waitExec = false
		bridge.nextAt = time.Now().Add(bridgeTurnGap)
	}
}

func (bridge *vhostBridge) flushOperations() int {
	sent := 0
	for _, operation := range bridge.takeOperations() {
		sent++
		side, data := operation.side, operation.data

		if binary.BigEndian.Uint32(data[8:12]) == bridgeRotateMon {
			bridge.setFront(side, rotateFront(bridge.front[side], int(data[bridgeOpValue])))
			fmt.Printf("[YW2-VHOST] side %d rotated, front starts at %d\n", side, bridge.front[side])
		}

		bridge.fillSpecialTarget(side, data)

		random := randomBytes(4)
		bridge.n++
		bridge.sides[side].sendGameLocked(piaKindOrder, buildOperation(bridge.n, data, false, random))
		bridge.sides[1-side].sendGameLocked(piaKindOrder, buildOperation(bridge.n, data, true, random))
		fmt.Printf("[YW2-VHOST] n=%d side %d operation %x random %x\n", bridge.n, side, data[8:12], random)
	}
	return sent
}

// * 開始前の回転の知らせ。[76] に前の 3 体の先頭の位置が入る
func startFront(data []byte) int {
	if len(data) <= bridgeStartFront {
		return 0
	}
	return int(data[bridgeStartFront]) % bridgeTeamMon
}

// * 持ち主の形の知らせを作る。参加者の要求には [90] など別の値が入っているので、そのまま渡さない
func buildTurnStart(front int) []byte {
	data := make([]byte, bridgeTurnSize)
	data[4] = bridgeTurnStart
	data[bridgeStartFront] = byte(front)
	return data
}

// * 報告の後列の印（0x10）から、前の 3 体の先頭を読む。読めなければ false
func frontFromState(data []byte) (int, bool) {
	front := -1
	count := 0
	for position := 0; position < bridgeTeamMon; position++ {
		if data[position*piaStateEntrySize+bridgeStateRow]&bridgeStateBack != 0 {
			continue
		}
		count++
		previous := (position + bridgeTeamMon - 1) % bridgeTeamMon
		if data[previous*piaStateEntrySize+bridgeStateRow]&bridgeStateBack != 0 {
			if front >= 0 {
				return 0, false
			}
			front = position
		}
	}
	if count != 3 || front < 0 {
		return 0, false
	}
	return front, true
}

// * 回転の確定の値（[72]、[26] の位置と同じ）は、回したあとの前の 3 体の先頭。1 つずつではなく、何個でも回せる。
// * Ｅの記録（2026-11-10 13:39）: 前が 0,1,2 のとき値 3 で回すと、そのあと位置 3,4,5 が動いた
func rotateFront(start int, entering int) int {
	return entering % bridgeTeamMon
}

func buildOperation(n uint32, request []byte, mirror bool, random []byte) []byte {
	data := append([]byte(nil), request[:bridgeOrderSize]...)
	binary.LittleEndian.PutUint32(data, n)

	actorIndex, actorTeam := request[24], request[25]
	targetIndex, targetTeam := request[26], request[27]
	if mirror {
		if actorTeam != 0 {
			actorIndex, actorTeam = mirrorSlot(actorIndex, actorTeam)
		}
		if targetTeam != 0 {
			targetIndex, targetTeam = mirrorSlot(targetIndex, targetTeam)
		}
		data[24], data[25], data[26], data[27] = actorIndex, actorTeam, targetIndex, targetTeam
	}

	if actorTeam != 0 && actorIndex < bridgeSlotCount {
		copy(data[16:], slotHandle(actorIndex, actorTeam))
	}
	if targetTeam != 0 && targetIndex < bridgeSlotCount {
		copy(data[20:], slotHandle(targetIndex, targetTeam))
	}

	// * G 技は元の行動の妖怪も入っている（[36:40] 番号、[40:42] 位置）
	extraIndex, extraTeam := request[40], request[41]
	if extraTeam != 0 && extraIndex < bridgeSlotCount {
		if mirror {
			extraIndex, extraTeam = mirrorSlot(extraIndex, extraTeam)
			data[40], data[41] = extraIndex, extraTeam
		}
		copy(data[36:], slotHandle(extraIndex, extraTeam))
	}

	copy(data[64:], random)
	// * [86] の 0x01 は「参加者の操作」の印。相手の本体から見れば持ち主の操作なので消す。
	// * 0x20 は G 技の印らしく、残す。消すと相手の本体では普通の必殺技になった
	if mirror {
		data[86] &^= 0x01
	}
	return data
}

func (bridge *vhostBridge) onAction(side int, data []byte) {
	// * 要求の [72:74] は「ねらう」の相手（確定の [60:62] と同じ。0x1002 は 12 バイトずれる）
	bridge.noteAim(side, data, bridgeActionAim)
	if !bridge.running || !bridge.waitAction || side != bridge.actingSide || len(data) < 36 {
		return
	}
	bridge.waitAction = false

	// * 技の値が 0 は行動しないという返事（溜めている妖怪など）。確定を出さず、次の番へ
	hash := data[8:12]
	if binary.LittleEndian.Uint32(hash) == 0 {
		fmt.Printf("[YW2-VHOST] side %d passed\n", side)
		bridge.finishTurn(side, bridge.actingPosition)
		bridge.nextAt = time.Now().Add(bridgeTurnGap)
		return
	}
	bridge.actingSkill = hexID(hash)
	actorIndex, actorTeam := data[32], data[33]
	targetIndex, targetTeam := data[34], data[35]
	random := randomBytes(4)

	bridge.n++
	mirrorActor, mirrorActorTeam := mirrorSlot(actorIndex, actorTeam)
	mirrorTarget, mirrorTargetTeam := mirrorSlot(targetIndex, targetTeam)

	own := buildOrder(bridge.n, hash, actorIndex, actorTeam, targetIndex, targetTeam, random)
	seen := buildOrder(bridge.n, hash, mirrorActor, mirrorActorTeam, mirrorTarget, mirrorTargetTeam, random)
	copyActionDetails(own, seen, data)

	bridge.sides[side].sendGameLocked(piaKindOrder, own)
	host := bridge.sides[1-side]
	host.sendGameLocked(piaKindTurn, buildTurn(mirrorActor, mirrorActorTeam))
	host.sendGameLocked(piaKindOrder, seen)

	fmt.Printf("[YW2-VHOST] n=%d side %d hash %x slot %d/%d -> %d/%d random %x\n", bridge.n, side, hash, actorIndex, actorTeam, targetIndex, targetTeam, random)
	bridge.waitN = bridge.n
	bridge.waitHash = append([]byte(nil), hash...)
	bridge.state[0].lastHash = nil
	bridge.state[1].lastHash = nil
	bridge.waitState = true
	bridge.waitedAt = time.Now()
}

func (bridge *vhostBridge) onState(side int, data []byte) {
	if len(data) != bridgeStateSize {
		return
	}

	state := &bridge.state[side]
	state.hp = readStateHP(data)
	state.flags = make([]uint32, bridgeTeamMon)
	state.status = make([]uint32, bridgeTeamMon)
	state.extra = make([]uint32, bridgeTeamMon)
	for position := range state.flags {
		entry := data[position*piaStateEntrySize:]
		state.flags[position] = binary.LittleEndian.Uint32(entry[bridgeStateRow:])
		state.status[position] = binary.LittleEndian.Uint32(entry[bridgeStateRow+4:])
		state.extra[position] = binary.LittleEndian.Uint32(entry[bridgeStateRow+8:])
	}
	if state.speed == nil {
		state.speed = make([]int, 12)
		for n := range state.speed {
			state.speed[n] = int(binary.LittleEndian.Uint16(data[n*piaStateEntrySize+bridgeStateSpeed:]))
		}
		state.plainSpeed = make([]int, bridgeTeamMon)
		for n := range state.plainSpeed {
			state.plainSpeed[n] = int(binary.LittleEndian.Uint16(data[n*piaStateEntrySize+bridgeStateSpeed+8:]))
		}
	}
	state.lastN = stateOrder(data)
	state.count++
	if bridge.waitOps && bridge.state[0].count > bridge.opsBase[0] && bridge.state[1].count > bridge.opsBase[1] {
		bridge.waitOps = false
		bridge.nextAt = time.Now().Add(bridgeTurnGap)
	}
	state.lastHash = append([]byte(nil), data[bridgeStateHash:bridgeStateHash+4]...)
	if front, ok := frontFromState(data); ok && front != bridge.front[side] {
		bridge.setFront(side, front)
	}

	// * 2 人の報告を比べる。片方の「本人の側」は、もう片方の「相手の側」
	report := bridge.reports[state.lastN]
	if report[side] == nil {
		report[side] = state.hp
		bridge.reports[state.lastN] = report
		if report[1-side] != nil {
			if same, detail := compareReports(report[0], report[1]); same {
				fmt.Printf("[YW2-VHOST] n=%d reports MATCH %v\n", state.lastN, report[0])
			} else {
				fmt.Printf("[YW2-VHOST] n=%d reports DIFFER %s\n", state.lastN, detail)
			}
		}
	}

	raw := bridge.rawReports[state.lastN]
	if raw[side] == nil {
		raw[side] = append([]byte(nil), data...)
		bridge.rawReports[state.lastN] = raw
		if raw[1-side] != nil {
			for _, line := range stateEntryDiffs(raw[0], raw[1]) {
				fmt.Printf("[YW2-VHOST] n=%d entry %s\n", state.lastN, line)
			}
		}
	}

	bridge.onExecReport(side, data)

	// * 2 人の本体が今の行動を終えて、その技の値を報告で返すまで、次の番を出さない
	if bridge.waitState && bytes.Equal(bridge.state[0].lastHash, bridge.waitHash) && bytes.Equal(bridge.state[1].lastHash, bridge.waitHash) {
		bridge.waitState = false
		bridge.finishTurn(bridge.actingSide, bridge.actingPosition)
		bridge.nextAt = time.Now().Add(bridgeTurnGap)
	}
}

// * 次の番を出す直前に、2 人の最新の報告を比べる。ここでずれていれば、一時的な遅れではない
func (bridge *vhostBridge) checkTurnReports() (same bool, checked bool) {
	if bridge.state[0].hp == nil || bridge.state[1].hp == nil || bridge.checkedN == bridge.n {
		return false, false
	}
	bridge.checkedN = bridge.n
	same, detail := compareReports(bridge.state[0].hp, bridge.state[1].hp)
	if same {
		fmt.Printf("[YW2-VHOST] n=%d before turn SAME\n", bridge.n)
	} else {
		fmt.Printf("[YW2-VHOST] n=%d before turn DESYNC %s\n", bridge.n, detail)
	}
	return same, true
}

func compareReports(one []uint16, two []uint16) (bool, string) {
	same := true
	for n := 0; n < bridgeTeamMon; n++ {
		if one[n] != two[n+bridgeTeamMon] || two[n] != one[n+bridgeTeamMon] {
			same = false
		}
	}
	return same, fmt.Sprintf("side0 %v side1 %v", one, two)
}

// * 2 人の報告で、同じ妖怪の 40 バイトが違うところ。side0 の i 番目は side1 の (i+6)%12 番目
func stateEntryDiffs(one []byte, two []byte) []string {
	var out []string
	for n := 0; n < 2*bridgeTeamMon; n++ {
		m := (n + bridgeTeamMon) % (2 * bridgeTeamMon)
		a := one[n*piaStateEntrySize : (n+1)*piaStateEntrySize]
		b := two[m*piaStateEntrySize : (m+1)*piaStateEntrySize]
		if !bytes.Equal(a, b) {
			out = append(out, fmt.Sprintf("side0[%d] %x side1[%d] %x", n, a, m, b))
		}
	}
	return out
}

// * 全滅したチーム。0、1、なければ -1
func (bridge *vhostBridge) defeatedSide() int {
	for side := range bridge.state {
		hp := bridge.state[side].hp
		if hp == nil {
			continue
		}
		alive := false
		for n := 0; n < bridgeTeamMon; n++ {
			if hp[n] != 0 {
				alive = true
			}
		}
		if !alive {
			return side
		}
	}
	return -1
}

// * 素早さの段。ゲームの表（94 段）と同じ値になる形。素早さ以上の最初の段を使う
func bridgeWaitRow(speed int) (int, int, int) {
	row := bridgeWaitRows - 1
	for n := 0; n < bridgeWaitRows; n++ {
		if bridgeWaitThreshold(n) >= speed {
			row = n
			break
		}
	}

	low := 156 - row
	if row > 66 {
		low = 90 - 2*(row-66)
	}
	high := 187 - row
	if row > 53 {
		high = 134 - 2*(row-53)
	}
	return low, high, 123 - row
}

func bridgeWaitThreshold(row int) int {
	switch {
	case row == 0:
		return 1
	case row <= 56:
		return 5 + 3*(row-1)
	case row <= 62:
		return 170 + 5*(row-56)
	case row <= 92:
		return 200 + 10*(row-62)
	}
	return 999
}

// * 妖怪の素早さ。本人の報告と相手の報告の小さいほう（片方の偽の報告だけでは速くならない）
func (bridge *vhostBridge) speedOf(side int, position int) int {
	speed := -1
	if own := bridge.state[side].speed; own != nil {
		speed = own[position]
	}
	if seen := bridge.state[1-side].speed; seen != nil && (speed < 0 || seen[position+bridgeTeamMon] < speed) {
		speed = seen[position+bridgeTeamMon]
	}
	if speed < 0 {
		return bridgeDefaultSpeed
	}
	return speed
}

// * パーティの通信から、素早さの効果を読む
func (bridge *vhostBridge) readParty(side int, data []byte, tables historyTables) {
	state := &bridge.state[side]
	state.units = make([]bridgeUnit, bridgeTeamMon)
	bridge.stages = tables.SpeedStages
	bridge.tribeBonus = tables.SpeedBonus
	bridge.specials = tables.Specials
	bridge.resetSkills = make(map[string]bool, len(tables.SpeedReset))
	for _, skill := range tables.SpeedReset {
		bridge.resetSkills[skill] = true
	}
	if len(data) < bridgeTeamMon*bridgePartyRecord {
		return
	}
	for n := range state.units {
		record := data[n*bridgePartyRecord : (n+1)*bridgePartyRecord]
		unit := &state.units[n]
		unit.effect = tables.SpeedSpecies[hexID(record[bridgePartySpecies:])]
		for slot := 0; slot < 2; slot++ {
			levels := tables.SpeedItems[hexID(record[bridgePartyItem+slot*4:])]
			if len(levels) > 0 {
				level := min(max(int(record[bridgePartySoulLevel]), 1), len(levels))
				unit.item = addSpeedEffect(unit.item, levels[level-1])
			}
		}
		unit.house = int(binary.LittleEndian.Uint16(record[bridgePartyHouse:])>>11) & 7
		unit.tribe = tables.SpeedTribes[hexID(record[bridgePartySpecies:])]
		unit.present = binary.LittleEndian.Uint32(record[bridgePartySpecies:]) != 0
	}
}

func addSpeedEffect(one speedEffect, two speedEffect) speedEffect {
	one.Flat += two.Flat
	one.Minus += two.Minus
	one.Neighbor += two.Neighbor
	one.Center += two.Center
	one.BackRow += two.BackRow
	one.All += two.All
	one.House += two.House
	one.StatusMinus += two.StatusMinus
	one.SameStage += two.SameStage
	one.CountStage += two.CountStage
	one.First = one.First || two.First
	one.Immune = one.Immune || two.Immune
	one.NoItems = one.NoItems || two.NoItems
	one.NoAbility = one.NoAbility || two.NoAbility
	one.Shuffle = one.Shuffle || two.Shuffle
	one.Taunt = one.Taunt || two.Taunt
	one.Stealth = one.Stealth || two.Stealth
	return one
}

// * 場のルール（FUN_00653bc8, FUN_00653cd4）。特性を無効にする効果があると持ち物は無効にならず、本人の特性が消える
func (bridge *vhostBridge) fieldRule() (noAbility bool, noItems bool) {
	for team := range bridge.state {
		for position := 0; position < bridgeTeamMon; position++ {
			if !bridge.alive(team, position) {
				continue
			}
			unit := bridge.unit(team, position)
			noAbility = noAbility || unit.effect.NoAbility || unit.item.NoAbility
			noItems = noItems || unit.effect.NoItems || unit.item.NoItems
		}
	}
	return noAbility, noItems && !noAbility
}

func (bridge *vhostBridge) effectOf(side int, position int) speedEffect {
	unit := bridge.unit(side, position)
	noAbility, noItems := bridge.fieldRule()
	var effect speedEffect
	if !noAbility {
		effect = unit.effect
	}
	if !noItems {
		effect = addSpeedEffect(effect, unit.item)
	}
	return effect
}

func (bridge *vhostBridge) unit(side int, position int) bridgeUnit {
	if units := bridge.state[side].units; units != nil {
		return units[position]
	}
	return bridgeUnit{}
}

func (bridge *vhostBridge) alive(side int, position int) bool {
	hp := bridge.state[side].hp
	return hp == nil || hp[position] != 0
}

// * 今の素早さ（FUN_00658708）。持ち物込みの報告の値に、並び、特性、魂、状態の効果を足す。
// * 回数で変わる特性（0x8CF0E169）はまだ入っていない
func (bridge *vhostBridge) currentSpeed(side int, position int) int {
	me := bridge.effectOf(side, position)
	house := bridge.unit(side, position).house
	previous, next := (position+bridgeTeamMon-1)%bridgeTeamMon, (position+1)%bridgeTeamMon

	var plus float32
	for _, neighbor := range []int{next, previous} {
		if bridge.alive(side, neighbor) {
			plus += bridge.effectOf(side, neighbor).Neighbor
		}
	}
	if position == (bridge.front[side]+1)%bridgeTeamMon {
		plus += me.Center
	}
	if bridge.alive(side, position) && bridge.inFront(side, position) {
		for other := 0; other < bridgeTeamMon; other++ {
			if bridge.alive(side, other) && !bridge.inFront(side, other) {
				plus += bridge.effectOf(side, other).BackRow
			}
		}
	}
	for team := range bridge.state {
		for other := 0; other < bridgeTeamMon; other++ {
			if !bridge.alive(team, other) {
				continue
			}
			effect := bridge.effectOf(team, other)
			plus += effect.All
			if house == bridgeHouseMain {
				plus += effect.House
			}
		}
	}

	minus := me.Minus
	status := bridge.statusOf(side, position)
	if status&bridgeStatusBad != 0 {
		minus += me.StatusMinus
	}

	value := float32(int(float32(bridge.baseSpeed(side, position)) + me.Flat))
	speed := int(value + plus*0.01*value - minus*0.01*value)

	// * 段（状態と、隣と同じ特性）。-8 から +8 で、段の表の % をかける
	stage := 0
	if status&bridgeStatusSpeedUp != 0 {
		stage += bridgeStatusStage
	}
	if status&bridgeStatusSpeedDown != 0 && !me.Immune {
		stage -= bridgeStatusStage
	}
	if me.SameStage != 0 && ((bridge.alive(side, next) && bridge.effectOf(side, next).SameStage != 0) || (bridge.alive(side, previous) && bridge.effectOf(side, previous).SameStage != 0)) {
		stage += me.SameStage
	}
	if me.CountStage != 0 {
		for _, neighbor := range []int{next, previous} {
			if bridge.alive(side, neighbor) && bridge.effectOf(side, neighbor).CountStage != 0 {
				stage += me.CountStage
			}
		}
	}
	if stage != 0 && len(bridge.stages) == bridgeStageCount {
		stage = min(max(stage, -bridgeStageMax), bridgeStageMax)
		change := speed * (bridge.stages[stage+bridgeStageMax] - 100) / 100
		if change == 0 {
			change = 1
			if bridge.stages[stage+bridgeStageMax] < 100 {
				change = -1
			}
		}
		speed += change
	}

	// * 種族の効果（FUN_0065a670）。前の妖怪だけ
	if percent := bridge.tribePercent(side); percent != 0 && bridge.alive(side, position) && bridge.inFront(side, position) {
		speed += max(speed*percent/100, 1)
	}
	return max(speed, 1)
}

// * 種族の効果の %（FUN_00653414）。前の真ん中の妖怪の種族と、両隣に同じ種族が生きている数で決まる
func (bridge *vhostBridge) tribePercent(side int) int {
	center := (bridge.front[side] + 1) % bridgeTeamMon
	if !bridge.alive(side, center) {
		return 0
	}
	tribe := bridge.unit(side, center).tribe
	if tribe == 0 {
		return 0
	}
	count := 1
	for _, neighbor := range []int{(center + 1) % bridgeTeamMon, (center + bridgeTeamMon - 1) % bridgeTeamMon} {
		if bridge.alive(side, neighbor) && bridge.unit(side, neighbor).tribe == tribe {
			count++
		}
	}
	for _, row := range bridge.tribeBonus {
		if row[0] == tribe && row[1] == count {
			return row[2]
		}
	}
	return 0
}

// * ゲームの乱数（FUN_00617124, FUN_005561b4, FUN_00617198）
type bridgeRandom [4]uint32

func newBridgeRandom(seed uint32) *bridgeRandom {
	random := &bridgeRandom{0x6C078966, 0xDD5254A5, 0xB9523B81, 0x03DF95B3}
	if seed != 0 {
		value := (seed^seed>>30)*0x6C078965 + 1
		random[0] = value
		value = (value^value>>30)*0x6C078965 + 2
		random[1] = value
		random[2] = (value^value>>30)*0x6C078965 + 3
	}
	for n := 0; n < 10; n++ {
		random.next()
	}
	return random
}

func (random *bridgeRandom) next() uint32 {
	first, last := random[0], random[3]
	random[0], random[1], random[2] = random[1], random[2], last
	first ^= first << 11
	random[3] = last ^ last>>19 ^ first ^ first>>8
	return random[3]
}

// * 0 以上 count 未満（FUN_0055715c）
func (random *bridgeRandom) index(count int) int {
	value := float32(random.next()&0xFFFFFF) / 16777216
	return min(int(value*float32(count)), count-1)
}

// * 並びの入れ替え（FUN_00552b14）。前から順に、乱数で選んだ位置と入れ替える。結果は位置ごとの元の位置
func (random *bridgeRandom) shuffle(count int) []int {
	order := make([]int, count)
	for n := range order {
		order[n] = n
	}
	for n := range order {
		other := random.index(count)
		order[n], order[other] = order[other], order[n]
	}
	return order
}

func sameOrder(one []int, two []int) bool {
	for n := range one {
		if one[n] != two[n] {
			return false
		}
	}
	return true
}

// * 本物の 2 人は持ち主の側から順に乱数を引く。サーバーの部屋では 2 人とも相手を持ち主と思うので、
// * 引く順が逆になる。2 回の並びが同じになるシードを選ぶと、2 台の見え方がそろう
func findShuffleSeed(countOne int, countTwo int, start uint32) (uint32, []int, []int, bool) {
	for tries := uint32(0); tries < bridgeSeedTries; tries++ {
		seed := start + tries
		if seed == 0 {
			continue
		}
		one := newBridgeRandom(seed)
		firstOne, firstTwo := one.shuffle(countOne), one.shuffle(countTwo)
		two := newBridgeRandom(seed)
		secondTwo, secondOne := two.shuffle(countTwo), two.shuffle(countOne)
		if sameOrder(firstOne, secondOne) && sameOrder(firstTwo, secondTwo) {
			return seed, firstOne, firstTwo, true
		}
	}
	return 0, nil, nil, false
}

// * 場につらがわりがいれば、シードを選び直して、サーバーの持つ並びも同じように入れ替える
func (bridge *vhostBridge) prepareShuffle() {
	shuffled := false
	for side := range bridge.state {
		for position := 0; position < bridgeTeamMon; position++ {
			unit := bridge.unit(side, position)
			if unit.effect.Shuffle || unit.item.Shuffle {
				shuffled = true
			}
		}
	}
	if !shuffled {
		return
	}

	present := [2][]int{}
	for side := range bridge.state {
		for position := 0; position < bridgeTeamMon; position++ {
			if bridge.unit(side, position).present {
				present[side] = append(present[side], position)
			}
		}
	}
	seed, orderOne, orderTwo, ok := findShuffleSeed(len(present[0]), len(present[1]), binary.LittleEndian.Uint32(bridge.seed))
	if !ok {
		fmt.Printf("[YW2-VHOST] no shuffle seed found\n")
		return
	}
	binary.LittleEndian.PutUint32(bridge.seed, seed)
	for side, order := range [][]int{orderOne, orderTwo} {
		units := append([]bridgeUnit(nil), bridge.state[side].units...)
		for n, from := range order {
			units[present[side][n]] = bridge.state[side].units[present[side][from]]
		}
		bridge.state[side].units = units
	}
	fmt.Printf("[YW2-VHOST] party shuffle, seed %x order %v %v\n", seed, orderOne, orderTwo)
}

func (bridge *vhostBridge) statusOf(side int, position int) uint32 {
	state := &bridge.state[side]
	if state.status == nil {
		return 0
	}
	return state.status[position]
}

// * 報告の値。持ち物が無効の間は、持ち物なしの 2 組目
func (bridge *vhostBridge) baseSpeed(side int, position int) int {
	_, noItems := bridge.fieldRule()
	if noItems {
		if plain := bridge.state[side].plainSpeed; plain != nil {
			return plain[position]
		}
	}
	return bridge.speedOf(side, position)
}

// * 行動のあとの待ち時間。技によらず 3 倍（表の値が 0 の技は開始前の操作だけ）
func (bridge *vhostBridge) fullWait(side int, position int) int {
	_, _, step := bridgeWaitRow(bridge.currentSpeed(side, position))
	return bridgeWaitSteps * step
}

func (bridge *vhostBridge) inFront(side int, position int) bool {
	return (position+bridgeTeamMon-bridge.front[side])%bridgeTeamMon < 3
}

// * 動けない状態（FUN_00657a38）。溜めのほか、行動できない状態も同じ。残りは止まる
func (bridge *vhostBridge) charging(side int, position int) bool {
	state := &bridge.state[side]
	if state.flags == nil {
		return false
	}
	mask := uint32(bridgeCannotAct)
	if state.extra != nil {
		switch {
		case state.extra[position]&bridgeExtraStrong != 0:
			mask = bridgeCannotActStrong
		case state.extra[position]&bridgeExtraMiddle != 0:
			mask = bridgeCannotActMiddle
		}
	}
	return state.flags[position]&mask != 0
}

func (bridge *vhostBridge) setWait(side int, position int, wait int) {
	bridge.wait[side][position] = wait
	bridge.queued++
	bridge.waitOrder[side][position] = bridge.queued
}

// * 開始時。前の 3 体だけ、表の 1 列目と 2 列目の間の乱数を待ち時間にする。後列はまだ持たない
func (bridge *vhostBridge) startClocks() {
	for side := range bridge.wait {
		for position := range bridge.wait[side] {
			bridge.wait[side][position] = bridgeNoWait
			if bridge.inFront(side, position) {
				low, high, _ := bridgeWaitRow(bridge.currentSpeed(side, position))
				bridge.setWait(side, position, bridgeFirstWait(low, high))
				bridge.goesFirst[side][position] = bridge.effectOf(side, position).First
			}
		}
	}
	bridge.clocks = true
}

// * 行動が終わった妖怪は列に並び直す。行動中に後列へ下がった妖怪も、戻ったときはこの待ち時間から
func (bridge *vhostBridge) finishTurn(side int, position int) {
	if !bridge.clocks {
		return
	}
	bridge.setWait(side, position, bridge.fullWait(side, position))

	// * 味方の残りを入れ直す技（FUN_0057af64）。味方の前の妖怪で、残りを持っている妖怪だけ
	if bridge.resetSkills[bridge.actingSkill] {
		for other := 0; other < bridgeTeamMon; other++ {
			if other != position && bridge.alive(side, other) && bridge.inFront(side, other) && bridge.wait[side][other] != bridgeNoWait {
				bridge.setWait(side, other, bridge.fullWait(side, other))
			}
		}
	}
	bridge.actingSkill = ""
}

// * 前の 3 体を変える。後列の待ち時間は止まっていて、戻るとそこから続く。
// * はじめて前に出る妖怪は、行動のあとと同じだけ待つ
func (bridge *vhostBridge) setFront(side int, front int) {
	bridge.front[side] = front
	if !bridge.clocks {
		return
	}
	for position := range bridge.wait[side] {
		if bridge.inFront(side, position) && bridge.wait[side][position] == bridgeNoWait {
			bridge.setWait(side, position, bridge.fullWait(side, position))
		}
	}
}

// * 番を回せる位置。前の 3 体のうち、生きていて溜めていない妖怪。
// * 前が全滅なら生きている妖怪（本体の回転が届くまでのつなぎ）
func (bridge *vhostBridge) eligible(side int) []int {
	start := bridge.front[side]
	state := &bridge.state[side]
	alive := func(position int) bool {
		return state.hp == nil || state.hp[position] != 0
	}
	charging := func(position int) bool {
		return bridge.charging(side, position)
	}

	out := []int{}
	frontAlive := false
	for offset := 0; offset < 3; offset++ {
		position := (start + offset) % bridgeTeamMon
		if !alive(position) {
			continue
		}
		frontAlive = true
		if !charging(position) {
			out = append(out, position)
		}
	}
	if frontAlive {
		return out
	}
	for position := 0; position < bridgeTeamMon; position++ {
		if alive(position) && !charging(position) {
			out = append(out, position)
		}
	}
	return out
}

// * 開始時の先頭の印（閃光魂）。ゲームの FUN_0057b3f0 は、印のある妖怪を残りの値によらず前へ並べる
func (bridge *vhostBridge) ahead(side int, position int, bestSide int, bestPosition int) bool {
	first, bestFirst := bridge.goesFirst[side][position], bridge.goesFirst[bestSide][bestPosition]
	if first != bestFirst {
		return first
	}
	wait, best := bridge.wait[side][position], bridge.wait[bestSide][bestPosition]
	if wait != best {
		return wait < best
	}
	return bridge.waitOrder[side][position] < bridge.waitOrder[bestSide][bestPosition]
}

// * 次に動く妖怪。残りのいちばん少ない妖怪で、同じなら先に並んだほう。
// * 実時間は関係ない。動く妖怪の残りの分だけ、前にいる全員の残りを減らす（溜めている妖怪は止まっている）
func (bridge *vhostBridge) nextActor() (int, int, bool) {
	bestSide, bestPosition := -1, -1
	for side := range bridge.state {
		for _, position := range bridge.eligible(side) {
			if bridge.wait[side][position] == bridgeNoWait {
				bridge.setWait(side, position, bridge.fullWait(side, position))
			}
			if bestSide < 0 || bridge.ahead(side, position, bestSide, bestPosition) {
				bestSide, bestPosition = side, position
			}
		}
	}
	if bestSide < 0 {
		return 0, 0, false
	}

	bridge.goesFirst[bestSide][bestPosition] = false
	passed := bridge.wait[bestSide][bestPosition]
	for side := range bridge.wait {
		for position := range bridge.wait[side] {
			if bridge.wait[side][position] == bridgeNoWait || !bridge.inFront(side, position) || bridge.charging(side, position) {
				continue
			}
			bridge.wait[side][position] = max(bridge.wait[side][position]-passed, 0)
		}
	}
	return bestSide, bestPosition, true
}

func (bridge *vhostBridge) startTurnLocked() {
	now := time.Now()
	sent := bridge.flushOperations()

	// * 発動の演出の間は番を出さない。終われば報告から再開する
	if bridge.waitExec {
		return
	}
	// * 本物は操作を確定したあと、2 人の報告が来てから次の番を出す（回転の演出の途中に番を送らない）
	if sent > 0 {
		bridge.waitOps = true
		bridge.opsBase = [2]int{bridge.state[0].count, bridge.state[1].count}
		bridge.waitedAt = now
		return
	}
	bridge.checkTurnReports()

	if defeated := bridge.defeatedSide(); defeated >= 0 {
		fmt.Printf("[YW2-VHOST] side %d defeated\n", defeated)
		for _, host := range bridge.sides {
			host.sendGameLocked(piaKindTurn, buildTurnEnd())
		}
		bridge.running = false
		bridge.ended = true
		return
	}

	// * 動ける妖怪がいなければ、少し待ってもう一度見る
	defer func() {
		if !bridge.waitAction && bridge.running {
			bridge.nextAt = now.Add(bridgeTurnGap)
		}
	}()

	// * 素早さは最初の報告で分かる。来なくても長くは待たせない
	if !bridge.clocks {
		if (bridge.state[0].speed == nil || bridge.state[1].speed == nil) && now.Sub(bridge.startedAt) < bridgeWaitSpeed {
			return
		}
		bridge.startClocks()
	}

	side, position, ok := bridge.nextActor()
	if !ok {
		return
	}
	index := byte(position) + bridgeTeamMon
	bridge.actingSide = side
	bridge.actingPosition = position
	bridge.sides[side].sendGameLocked(piaKindTurn, buildTurn(index, 2))
	bridge.waitAction = true
	bridge.waitedAt = now
	fmt.Printf("[YW2-VHOST] turn side %d slot %d speed %d\n", side, index, bridge.currentSpeed(side, position))
}

func (bridge *vhostBridge) tick() {
	for now := range time.Tick(50 * time.Millisecond) {
		bridge.mutex.Lock()
		bridge.tickLocked(now)
		bridge.mutex.Unlock()
	}
}

func (bridge *vhostBridge) tickLocked(now time.Time) {
	if !bridge.running {
		return
	}

	// * 返事がなければ番を飛ばす。報告が来なければ進める
	if bridge.waitAction && now.Sub(bridge.waitedAt) >= bridgeWaitAction {
		fmt.Printf("[YW2-VHOST] side %d did not act\n", bridge.actingSide)
		bridge.waitAction = false
		bridge.finishTurn(bridge.actingSide, bridge.actingPosition)
		bridge.nextAt = now
	}
	if bridge.waitState && now.Sub(bridge.waitedAt) >= bridgeWaitState {
		fmt.Printf("[YW2-VHOST] no report for n=%d: %d %d\n", bridge.waitN, bridge.state[0].lastN, bridge.state[1].lastN)
		bridge.waitState = false
		bridge.finishTurn(bridge.actingSide, bridge.actingPosition)
		bridge.nextAt = now
	}

	if bridge.waitOps && now.Sub(bridge.waitedAt) >= bridgeWaitOps {
		fmt.Printf("[YW2-VHOST] no report after the operations: %d %d\n", bridge.state[0].count-bridge.opsBase[0], bridge.state[1].count-bridge.opsBase[1])
		bridge.waitOps = false
		bridge.nextAt = now
	}

	if bridge.waitExec && now.Sub(bridge.waitedAt) >= bridgeWaitExec {
		fmt.Printf("[YW2-VHOST] no report after the special: %v\n", bridge.execSeen)
		bridge.waitExec = false
		bridge.nextAt = now
	}

	if !bridge.waitAction && !bridge.waitState && !bridge.waitExec && !bridge.waitOps && !bridge.nextAt.IsZero() && now.After(bridge.nextAt) {
		bridge.nextAt = time.Time{}
		bridge.startTurnLocked()
	}
}

const (
	bridgeActionAim     = 72     // * 0x1002 の「ねらう」の相手（位置、チーム）
	bridgeOrderAim      = 60     // * 0x1007 / 確定の「ねらう」の相手
	bridgeStatusTaunt   = 0x1000 // * 報告の状態（+0x3CC）: 挑発されている。狙われる（FUN_0065417c）
	bridgeStatusStealth = 0x2000 // * 報告の状態（+0x3CC）: おんみつ（FUN_00659090 の 0x8）
)

// * 「ねらう」の相手を覚える。0x1002 は毎回の今の指定なので 0 なら外す。0x1007 は入っているときだけ
func (bridge *vhostBridge) noteAim(side int, data []byte, offset int) {
	if len(data) < offset+2 {
		return
	}
	if offset == bridgeActionAim || data[offset+1] != 0 {
		bridge.aim[side] = [2]byte{data[offset], data[offset+1]}
	}
}

// * 必殺技の要求は狙いが空。空のままだと 2 台がそれぞれ選び直し（FUN_0058cf7c）、Ｅでは使う乱数の数が 2 台で違ってずれる。
// * 敵や味方を狙う技（効果の種類 1、2、4）は、サーバーが狙いを決めて入れる。自分（3）と両方（5）は選び直しでも 2 台が同じなので触らない。
// * 敵の決まりは物理の攻撃の表（0x70B484）の順: 挑発 → ねらう → おんみつ以外から乱数 → 全員から乱数（倒せる相手を優先する決まりは入れていない）
func (bridge *vhostBridge) fillSpecialTarget(side int, data []byte) {
	kind, ok := bridge.specials[hex.EncodeToString(data[8:12])]
	if !ok || data[27] != 0 {
		return
	}
	switch kind[0] {
	case 1:
		position, ok := bridge.chooseEnemy(side)
		if !ok {
			return
		}
		data[26], data[27] = byte(position), 1
	case 2, 4:
		var candidates []int
		for position := 0; position < bridgeTeamMon; position++ {
			if bridge.targetable(side, position) {
				candidates = append(candidates, position)
			}
		}
		if len(candidates) == 0 {
			return
		}
		data[26], data[27] = byte(candidates[randomIndex(len(candidates))]+bridgeTeamMon), 2
	default:
		return
	}
	fmt.Printf("[YW2-VHOST] side %d special %x kind %v target %d/%d\n", side, data[8:12], kind, data[26], data[27])
}

func (bridge *vhostBridge) chooseEnemy(side int) (int, bool) {
	team := 1 - side
	var all, taunt, open []int
	for position := 0; position < bridgeTeamMon; position++ {
		if !bridge.targetable(team, position) {
			continue
		}
		all = append(all, position)
		status := uint32(0)
		if bridge.state[team].status != nil {
			status = bridge.state[team].status[position]
		}
		effect := bridge.effectOf(team, position)
		if status&bridgeStatusTaunt != 0 || effect.Taunt {
			taunt = append(taunt, position)
		}
		if status&bridgeStatusStealth == 0 && !effect.Stealth {
			open = append(open, position)
		}
	}
	if len(taunt) > 0 {
		return taunt[randomIndex(len(taunt))], true
	}
	if aim := bridge.aim[side]; aim[1] == 1 && int(aim[0]) < bridgeTeamMon {
		for _, position := range open {
			if position == int(aim[0]) {
				return position, true
			}
		}
	}
	if len(open) > 0 {
		return open[randomIndex(len(open))], true
	}
	if len(all) > 0 {
		return all[randomIndex(len(all))], true
	}
	return 0, false
}

// * 狙える妖怪: 2 台の報告の両方で生きていて、倒れておらず、前の 3 体にいる（FUN_00657af4）
func (bridge *vhostBridge) targetable(team int, position int) bool {
	if !bridge.alive(team, position) || !bridge.inFront(team, position) {
		return false
	}
	if hp := bridge.state[1-team].hp; hp != nil && hp[position+bridgeTeamMon] == 0 {
		return false
	}
	if flags := bridge.state[team].flags; flags != nil && flags[position]&0x1 != 0 {
		return false
	}
	return true
}

func randomIndex(n int) int {
	return int(binary.LittleEndian.Uint32(randomBytes(4)) % uint32(n))
}

// * 試験（46_部屋主の交代の計画.md）: 2 人がそろったところで、片方の部屋の持ち主（サーバー）が落ちたことにする。
// * 部屋に「持ち主が抜けたら残りから選ぶ」（0x10）、「ほかの人が持ち主を取れる」（0x200）、「参加の知らせを全員に」（0x400）を立て、
// * サーバーは何も送らなくなる。本体が持ち主を引き継ぐか（UpdateSessionHost が来るか）、通信エラーで抜けるかを見る
func (bridge *vhostBridge) startMigrationProbe(side int) {
	host := bridge.sides[side]
	if globals.Postgres != nil && host.gid != 0 {
		if _, err := globals.Postgres.Exec(`UPDATE matchmaking.gatherings SET flags = flags | $2 WHERE id = $1`, host.gid, bridgeMigrateFlags); err != nil {
			fmt.Printf("[YW2-VHOST] migration flags failed: %v\n", err)
		}
	}
	host.silent = true
	fmt.Printf("[YW2-VHOST] migration probe: side %d room %d host goes silent\n", side, host.gid)
}

const bridgeMigrateFlags = 0x10 | 0x200 | 0x400
