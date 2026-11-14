package nex

// 対戦履歴（38_サーバー機能の検討.md）。YW2_HISTORY=true のときだけ動く。
// 名前は部屋を探す呼び出しから、パーティは中継のパケットから、スコアは UploadScore から取る。
// 記録するだけで、対戦は止めない。ただしイベントの対戦は、ルール違反と連戦を止める（event_rules.go）。

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

const (
	partyRecordSize   = 80
	partyMonCount     = 6
	partyCodeSize     = 102
	appBufferMaxName  = 24
	appBufferScore    = 0x18 // * バッファの中の今のスコア
	partyHeaderSkip   = 16   // * 長さの 4 バイトの位置から、1 体目の先頭まで
	uploadScoreSize   = 39
	uploadScoreOffset = 9
)

var partyLengthMark = []byte{0xE0, 0x01, 0x00, 0x00} // * 80 × 6
var appBufferMark = []byte{0xB8, 0x00, 0x00, 0x00}   // * 部屋のバッファの長さ

type historyEntry struct {
	Number int    `json:"number"`
	Name   string `json:"name"`
	Rank   int    `json:"rank"`
}

type historyTables struct {
	Species map[string]historyEntry `json:"species"`
	Items   map[string]historyEntry `json:"items"`

	// * 行動順の計算に使う素早さの効果。妖怪の特性と魂
	SpeedSpecies map[string]speedEffect   `json:"speedSpecies"`
	SpeedItems   map[string][]speedEffect `json:"speedItems"`  // * 魂の段ごと
	SpeedStages  []int                    `json:"speedStages"` // * 段 -8 から +8 の %
	SpeedReset   []string                 `json:"speedReset"`  // * 味方の残りを入れ直す技
	SpeedTribes  map[string]int           `json:"speedTribes"` // * 種族
	SpeedBonus   [][3]int                 `json:"speedBonus"`  // * 素早さの種族効果。種族、体数、%

	// * 必殺技の狙いの種類。通信の技の番号 → [相手の種類（1 敵、2 味方、3 自分、4、5 両方、6）, 範囲]
	Specials map[string][2]int `json:"specials"`
}

// * 値はすべて %（Flat だけ固定値）。ゲームの素早さの計算（FUN_0065942c）が見る効果
type speedEffect struct {
	Flat     float32 `json:"flat"`
	Minus    float32 `json:"minus"`    // * 自分が遅くなる
	Neighbor float32 `json:"neighbor"` // * 輪の上で隣の妖怪を速くする
	Center   float32 `json:"center"`   // * 自分が前の真ん中にいる間
	BackRow  float32 `json:"backRow"`  // * 後列にいる間、前の味方を速くする
	All      float32 `json:"all"`      // * 場の全員を速くする
	House    float32 `json:"house"`    // * 場の本家の妖怪を速くする
	First    bool    `json:"first"`    // * 開始時に先頭へ並ぶ（閃光魂）

	StatusMinus float32 `json:"statusMinus"` // * 状態異常の間、遅くなる
	SameStage   int     `json:"sameStage"`   // * 隣も同じ特性なら、段が上がる
	CountStage  int     `json:"countStage"`  // * 同じ特性の隣の数だけ、段が上がる
	Immune      bool    `json:"immune"`      // * 遅くなる状態が効かない
	NoItems     bool    `json:"noItems"`     // * 場の持ち物を無効にする
	NoAbility   bool    `json:"noAbility"`   // * 場の特性を無効にする
	Shuffle     bool    `json:"shuffle"`     // * 開始時に両チームの並びを入れ替える（つらがわり）
	Taunt       bool    `json:"taunt"`       // * 敵から狙われる（FUN_0065417c）
	Stealth     bool    `json:"stealth"`     // * おんみつ。敵から狙われない（FUN_00659090）
}

type historyPlayer struct {
	name  []byte
	score *int64
}

type battleHistory struct {
	mutex       sync.Mutex
	tables      historyTables
	players     map[uint32]historyPlayer
	modes       map[uint32]string    // * 今の対戦の種類。random、friend、event:<PID>[:<ルール>]
	last        map[uint32][]byte    // * 同じ内容を何度も保存しない
	partyAt     map[uint32]time.Time // * 今の部屋で最後にパーティが届いた時刻。連戦を見分ける
	declinedAt  map[uint32]time.Time // * 連戦を断ったことにした時刻
	resultSaved map[uint32]bool      // * 今の対戦で、イベントの勝敗を記録した（event_result.go）
}

var history *battleHistory

func historyEnabled() bool {
	return os.Getenv("YW2_HISTORY") == "true"
}

func startBattleHistory() {
	if !historyEnabled() {
		return
	}

	tables, err := loadHistoryTables("/history-tables.json")
	if err != nil {
		fmt.Printf("[YW2-HISTORY] not started: %v\n", err)
		return
	}

	_, err = globals.Postgres.Exec(`
		CREATE SCHEMA IF NOT EXISTS history;
		CREATE OR REPLACE FUNCTION history.sjis_name(raw bytea) RETURNS text AS $body$
		BEGIN
			RETURN convert_from(raw, 'SJIS');
		EXCEPTION WHEN others THEN
			RETURN NULL;
		END;
		$body$ LANGUAGE plpgsql IMMUTABLE;
		CREATE TABLE IF NOT EXISTS history.battles (
			id bigserial PRIMARY KEY,
			gathering_id bigint UNIQUE NOT NULL,
			first_seen timestamptz NOT NULL DEFAULT now(),
			score_at timestamptz,
			score_pid bigint,
			score bigint
		);
		CREATE TABLE IF NOT EXISTS history.players (
			battle_id bigint NOT NULL REFERENCES history.battles(id),
			pid bigint NOT NULL,
			name text,
			party_raw bytea,
			party_code bytea,
			code_error text,
			seen_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (battle_id, pid)
		);
		ALTER TABLE history.players ADD COLUMN IF NOT EXISTS score_before bigint;
		ALTER TABLE history.battles ADD COLUMN IF NOT EXISTS mode text;
		ALTER TABLE history.battles ADD COLUMN IF NOT EXISTS cut_reason text;
		ALTER TABLE history.battles ADD COLUMN IF NOT EXISTS state_pid bigint;
		ALTER TABLE history.battles ADD COLUMN IF NOT EXISTS state_won boolean;
		ALTER TABLE history.battles ADD COLUMN IF NOT EXISTS state_at timestamptz;
		CREATE TABLE IF NOT EXISTS history.score_log (
			id bigserial PRIMARY KEY,
			pid bigint NOT NULL,
			at timestamptz NOT NULL DEFAULT now(),
			event text NOT NULL,
			score bigint NOT NULL
		);
		CREATE INDEX IF NOT EXISTS score_log_pid_at ON history.score_log (pid, at);
	`)
	if err != nil {
		fmt.Printf("[YW2-HISTORY] not started: %v\n", err)
		return
	}

	history = &battleHistory{tables: tables, players: make(map[uint32]historyPlayer), modes: make(map[uint32]string), last: make(map[uint32][]byte), partyAt: make(map[uint32]time.Time), declinedAt: make(map[uint32]time.Time), resultSaved: make(map[uint32]bool)}
	fmt.Printf("[YW2-HISTORY] enabled, %d species, %d items\n", len(tables.Species), len(tables.Items))
}

func loadHistoryTables(path string) (historyTables, error) {
	var tables historyTables

	data, err := os.ReadFile(path)
	if err != nil {
		return tables, err
	}

	err = json.Unmarshal(data, &tables)
	return tables, err
}

// * 部屋を探す呼び出しと UploadScore を見る。secure.go の OnData から呼ぶ
func (h *battleHistory) onCall(packet nex.PacketInterface) {
	request := packet.RMCMessage()
	pid := uint32(packet.Sender().PID())

	// * フレンド対戦の状態の文字列にも名前が入っている。入る側は部屋のバッファを送らないので、ここから取る
	if request.ProtocolID == 109 && request.MethodID == 9 {
		if name := notificationName(request.Parameters); name != nil {
			h.mutex.Lock()
			player := h.players[pid]
			player.name = name
			h.players[pid] = player
			h.mutex.Unlock()
		}
		return
	}

	// * フレンド対戦の部屋を作る、入る。同じパーティでもまた保存する
	if (request.ProtocolID == 109 && request.MethodID == 6) || (request.ProtocolID == 109 && request.MethodID == 30) {
		h.mutex.Lock()
		delete(h.last, pid)
		delete(h.partyAt, pid)
		delete(h.declinedAt, pid)
		// * 部屋を作るのは誘う前なので、イベントならこのあと誘ったときに変わる。
		// * 入る側は、部屋を探したときに決まっている
		if request.MethodID == 6 || h.modes[pid] == "" {
			h.modes[pid] = "friend"
		}
		h.mutex.Unlock()

		if activeRelay != nil {
			activeRelay.unblock(pid)
		}
		return
	}

	// * 部屋を探す相手がイベント用の偽のプレイヤーなら、イベントの対戦
	if request.ProtocolID == 21 && request.MethodID == 22 && len(request.Parameters) >= 4 {
		owner := binary.LittleEndian.Uint32(request.Parameters)
		mode := "friend"
		if globals.IsEventBot(owner) {
			mode = fmt.Sprintf("event:%d", owner)
		}
		h.setMode(pid, mode)
		return
	}

	if request.ProtocolID == 109 && request.MethodID == 15 {
		// * エントリー中はイベントの対戦。統計とレートでは普通のランダムと分ける
		if bot, ok := globals.EventEntry(pid); ok {
			h.setMode(pid, fmt.Sprintf("event:%d", bot))
		} else {
			h.setMode(pid, "random")
		}

		if name, score := appBufferPlayer(request.Parameters); name != nil {
			h.mutex.Lock()
			h.players[pid] = historyPlayer{name: name, score: score}
			h.mutex.Unlock()

			if score != nil {
				go h.logScore(pid, "search", *score)
			}
		}

		// * 新しい部屋を探し始めたので、同じパーティでもまた保存する
		h.mutex.Lock()
		delete(h.last, pid)
		delete(h.partyAt, pid)
		delete(h.declinedAt, pid)
		h.mutex.Unlock()

		if activeRelay != nil {
			activeRelay.unblock(pid)
		}
		return
	}

	if request.ProtocolID == 112 && request.MethodID == 1 && len(request.Parameters) == uploadScoreSize {
		parameters := request.Parameters
		score := uint32(parameters[uploadScoreOffset]) | uint32(parameters[uploadScoreOffset+1])<<8 |
			uint32(parameters[uploadScoreOffset+2])<<16 | uint32(parameters[uploadScoreOffset+3])<<24
		go h.saveScore(pid, score)
	}
}

func (h *battleHistory) setMode(pid uint32, mode string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	h.modes[pid] = mode
	delete(h.resultSaved, pid)
}

// * UpdateNotificationData の文字列（base64）の先頭 16 バイトが名前（Shift-JIS）
func notificationName(parameters []byte) []byte {
	if len(parameters) < 14 {
		return nil
	}

	size := int(binary.LittleEndian.Uint16(parameters[12:]))
	if size < 2 || 14+size > len(parameters) {
		return nil
	}

	raw, err := base64.StdEncoding.DecodeString(string(parameters[14 : 14+size-1]))
	if err != nil || len(raw) < 16 {
		return nil
	}

	name := raw[:16]
	if end := bytes.IndexByte(name, 0); end >= 0 {
		name = name[:end]
	}
	if len(name) == 0 {
		return nil
	}

	return append([]byte(nil), name...)
}

// * 部屋のバッファの先頭にある、自分のゲーム内の名前（Shift-JIS）と今のスコア
func appBufferPlayer(parameters []byte) ([]byte, *int64) {
	at := bytes.Index(parameters, appBufferMark)
	if at < 0 || at+4+appBufferMaxName > len(parameters) {
		return nil, nil
	}

	var score *int64
	if at+4+appBufferScore+4 <= len(parameters) {
		data := parameters[at+4+appBufferScore:]
		value := int64(uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24)
		score = &value
	}

	name := parameters[at+4 : at+4+appBufferMaxName]
	if end := bytes.IndexByte(name, 0); end >= 0 {
		name = name[:end]
	}
	if len(name) == 0 {
		return nil, nil
	}

	return append([]byte(nil), name...), score
}

// * 試験で名前だけを確かめる
func appBufferName(parameters []byte) []byte {
	name, _ := appBufferPlayer(parameters)
	return name
}

// * 中継を通るパケットを見る。パーティのパケットなら保存する
func (h *battleHistory) onPacket(pid uint32, data []byte) {
	body := findPartyBody(data, h.tables)
	if body == nil {
		return
	}

	now := time.Now()

	h.mutex.Lock()
	mode := h.modes[pid]
	bot, isEvent := eventBotOfMode(mode)
	previous, seen := h.partyAt[pid]
	h.partyAt[pid] = now
	rematch := isEvent && seen && now.Sub(previous) >= eventRematchGap
	same := bytes.Equal(h.last[pid], body)
	if !same && !rematch {
		h.last[pid] = append([]byte(nil), body...)
	}
	player := h.players[pid]
	h.mutex.Unlock()

	// * 1 戦目の記録は残したまま止める
	if rematch {
		cutEventBattle(pid, eventCutRematch)
		go h.saveCut(pid, eventCutRematch)
		return
	}

	if same {
		return
	}

	cutReason := ""
	if isEvent {
		cutReason = eventRuleViolation(body, globals.EventRulesOf(bot), h.tables)
		if cutReason != "" {
			cutEventBattle(pid, cutReason)
		}
	}

	// * スコアはランダム対戦だけで増減する
	if mode != "random" && mode != "" {
		player.score = nil
	}

	code, codeError := encodePartyCode(body, h.tables)
	saved := append([]byte(nil), body...)
	go func() {
		h.saveParty(pid, player, mode, saved, code, codeError)
		if cutReason != "" {
			h.saveCut(pid, cutReason)
		}
	}()
}

// * 止めたあとに届いたパーティ。今の部屋でまだ届いていない人の分だけ返す。
// * 連戦で送り直されたものを保存すると、1 戦目の記録が消えてしまう
func (h *battleHistory) blockedParty(pid uint32, data []byte) []byte {
	body := findPartyBody(data, h.tables)
	if body == nil {
		return nil
	}

	h.mutex.Lock()
	defer h.mutex.Unlock()

	if _, seen := h.partyAt[pid]; seen {
		return nil
	}
	h.partyAt[pid] = time.Now()
	h.last[pid] = append([]byte(nil), body...)

	return append([]byte(nil), body...)
}

func (h *battleHistory) onBlockedPacket(pid uint32, data []byte) {
	body := h.blockedParty(pid, data)
	if body == nil {
		return
	}

	h.mutex.Lock()
	player := h.players[pid]
	mode := h.modes[pid]
	h.mutex.Unlock()

	if mode != "random" && mode != "" {
		player.score = nil
	}

	code, codeError := encodePartyCode(body, h.tables)
	go h.saveParty(pid, player, mode, body, code, codeError)
}

// * 止めた理由を残す。最初の理由だけ
func (h *battleHistory) saveCut(pid uint32, reason string) {
	battleID, err := historyBattleID(pid)
	if err != nil {
		fmt.Printf("[YW2-HISTORY] pid %d: no battle to cut: %v\n", pid, err)
		return
	}

	_, err = globals.Postgres.Exec(`UPDATE history.battles SET cut_reason = $2 WHERE id = $1 AND cut_reason IS NULL`, battleID, reason)
	if err != nil {
		fmt.Printf("[YW2-HISTORY] battle %d: cut reason failed: %v\n", battleID, err)
	}
}

func findPartyBody(data []byte, tables historyTables) []byte {
	at := bytes.Index(data, partyLengthMark)
	if at < 0 || at+partyHeaderSkip+partyRecordSize*partyMonCount > len(data) {
		return nil
	}

	body := data[at+partyHeaderSkip : at+partyHeaderSkip+partyRecordSize*partyMonCount]

	// * 6 体とも知っている妖怪のときだけ、パーティとみなす
	for n := 0; n < partyMonCount; n++ {
		record := body[partyRecordSize*n : partyRecordSize*(n+1)]
		if _, ok := tables.Species[hexID(record[0x20:0x24])]; !ok {
			return nil
		}
	}

	return body
}

func hexID(data []byte) string {
	value := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
	return strconv.FormatUint(uint64(value), 16)
}

func gymCounts(c []int) (int, bool) {
	for a := 0; a <= 5; a++ {
		for b := 0; a+b <= 5; b++ {
			for k := 0; a+b+k <= 5; k++ {
				for d := 0; a+b+k+d <= 5; d++ {
					if c[0] == 0 && c[1] == 5*a && c[2] == 5*b && c[3] == 5*k-2*(a+d) && c[4] == 5*d-2*(b+k) {
						return a | b<<3 | k<<6 | d<<9, true
					}
				}
			}
		}
	}

	return 0, false
}

func itemNumber(data []byte, tables historyTables) (int, bool) {
	if data[0]|data[1]|data[2]|data[3] == 0 {
		return 0, true
	}

	entry, ok := tables.Items[hexID(data)]
	return entry.Number, ok
}

// * パーティコード（102 バイト）を作る。形が合わない項目があれば、理由を返す
func encodePartyCode(body []byte, tables historyTables) ([]byte, string) {
	code := make([]byte, 0, partyCodeSize)
	code = append(code, 'Y', 'P', 1, 0)

	for n := 0; n < partyMonCount; n++ {
		record := body[partyRecordSize*n : partyRecordSize*(n+1)]

		species, ok := tables.Species[hexID(record[0x20:0x24])]
		if !ok {
			return nil, fmt.Sprintf("slot %d: unknown species", n+1)
		}

		item0, ok := itemNumber(record[0x0C:0x10], tables)
		if !ok {
			return nil, fmt.Sprintf("slot %d: unknown item 0", n+1)
		}

		item1, ok := itemNumber(record[0x10:0x14], tables)
		if !ok {
			return nil, fmt.Sprintf("slot %d: unknown item 1", n+1)
		}

		a := record[0x30:0x35]
		if a[0]%2 != 0 || int(a[0])/2+int(a[1])+int(a[2])+int(a[3])+int(a[4]) != 40 {
			return nil, fmt.Sprintf("slot %d: A is out of range", n+1)
		}

		c := make([]int, 5)
		for i := range c {
			c[i] = int(int8(record[0x3A+i]))
		}

		gym, ok := gymCounts(c)
		if !ok {
			return nil, fmt.Sprintf("slot %d: C is out of range", n+1)
		}

		side := int(record[0x41]>>3) & 3
		word := uint32(species.Number+1) | uint32(item0)<<10 | uint32(item1)<<19 | uint32(side)<<28

		code = append(code, byte(word), byte(word>>8), byte(word>>16), byte(word>>24))
		code = append(code, a[0]/2, a[1], a[2], a[3])
		code = append(code, record[0x35:0x3A]...)
		code = append(code, byte(gym), byte(gym>>8), record[0x40])
	}

	crc := partyCRC(code)
	return append(code, byte(crc), byte(crc>>8)), ""
}

func partyCRC(data []byte) uint32 {
	crc := uint32(0xFFFF)

	for _, value := range data {
		crc ^= uint32(value) << 8
		for i := 0; i < 8; i++ {
			crc <<= 1
			if crc&0x10000 != 0 {
				crc ^= 0x11021
			}
		}
	}

	return crc
}

// * pid の最新の部屋を探して、履歴の対戦の番号を返す
func historyBattleID(pid uint32) (int64, error) {
	var battleID int64

	err := globals.Postgres.QueryRow(`
		WITH latest AS (
			SELECT gathering_id FROM tracking.join_gathering
			WHERE source_pid = $1 ORDER BY date DESC LIMIT 1
		)
		INSERT INTO history.battles (gathering_id)
		SELECT gathering_id FROM latest
		ON CONFLICT (gathering_id) DO UPDATE SET gathering_id = EXCLUDED.gathering_id
		RETURNING id
	`, int64(pid)).Scan(&battleID)

	return battleID, err
}

func (h *battleHistory) saveParty(pid uint32, player historyPlayer, mode string, body []byte, code []byte, codeError string) {
	battleID, err := historyBattleID(pid)
	if err != nil {
		fmt.Printf("[YW2-HISTORY] pid %d: no battle: %v\n", pid, err)
		return
	}

	// * Shift-JIS のまま渡し、Postgres で変換する。変換できない名前は 16 進で残す
	_, err = globals.Postgres.Exec(`
		INSERT INTO history.players (battle_id, pid, name, party_raw, party_code, code_error, score_before)
		VALUES ($1, $2, COALESCE(history.sjis_name($3), encode($3, 'hex')), $4, $5, NULLIF($6, ''), $7)
		ON CONFLICT (battle_id, pid) DO UPDATE SET
			name = EXCLUDED.name, party_raw = EXCLUDED.party_raw,
			party_code = EXCLUDED.party_code, code_error = EXCLUDED.code_error,
			score_before = COALESCE(EXCLUDED.score_before, history.players.score_before)
	`, battleID, int64(pid), player.name, body, code, codeError, player.score)
	if err != nil {
		fmt.Printf("[YW2-HISTORY] pid %d: save failed: %v\n", pid, err)
		return
	}

	// * 2 人で情報の細かさが違うので、詳しいほうを残す（event:<PID>:<ルール> が一番詳しい）
	if mode != "" {
		_, err = globals.Postgres.Exec(`UPDATE history.battles SET mode = $2 WHERE id = $1 AND length(coalesce(mode, '')) < length($2)`, battleID, mode)
		if err != nil {
			fmt.Printf("[YW2-HISTORY] battle %d: mode failed: %v\n", battleID, err)
		}
	}

	fmt.Printf("[YW2-HISTORY] battle %d pid %d mode %s party saved, code ok=%t %s\n", battleID, pid, mode, codeError == "", codeError)
}

// * スコアが見えるたびに残す。いつ増減したかを後から追うため
func (h *battleHistory) logScore(pid uint32, event string, score int64) {
	_, err := globals.Postgres.Exec(`INSERT INTO history.score_log (pid, event, score) VALUES ($1, $2, $3)`, int64(pid), event, score)
	if err != nil {
		fmt.Printf("[YW2-HISTORY] pid %d: score log failed: %v\n", pid, err)
	}
}

func (h *battleHistory) saveScore(pid uint32, score uint32) {
	h.logScore(pid, "upload", int64(score))

	battleID, err := historyBattleID(pid)
	if err != nil {
		fmt.Printf("[YW2-HISTORY] pid %d: no battle for score: %v\n", pid, err)
		return
	}

	_, err = globals.Postgres.Exec(`UPDATE history.battles SET score_at = now(), score_pid = $2, score = $3 WHERE id = $1`, battleID, int64(pid), int64(score))
	if err != nil {
		fmt.Printf("[YW2-HISTORY] pid %d: score failed: %v\n", pid, err)
		return
	}

	fmt.Printf("[YW2-HISTORY] battle %d pid %d score %d\n", battleID, pid, score)
}
