package nex

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

const (
	// * 連戦はサーバーを通らず、対戦の通信だけで始まる。パーティが送り直されるので、それで見分ける。
	// * 同じ対戦の再送と間違えないように、少し間があいたものだけを数える。実際の連戦では 56 秒あいた
	eventRematchGap = 8 * time.Second

	eventCutRematch = "連戦"

	eventDeclineLimit = 15 * time.Second // * 断ったことにしてから、終わるのを待つ時間

	partyLevelOffset = 0x3F

	// * 対戦の通信の形。末尾 16 バイトは、部屋の session_key を鍵にした HMAC-MD5（全体のうち末尾以外）
	piaSignatureSize   = 16
	piaMessageHeadSkip = 8      // * 区切りのあと、番号と確認の 8 バイト
	piaMessageHeadSize = 12     // * 宛先 2、種類 2、番号 2、送信元 2、長さ 4
	piaMessageState    = 0x1008 // * 参加者の状態の報告。40 バイト × 12 体と末尾 24 バイト
	piaStateSize       = 504
	piaStateEntrySize  = 40
	piaStateMaxHP      = 2
	piaStateHP         = 4
	piaMessageRematch  = 0x1010 // * 連戦する
	piaMessageDecline  = 0x1011 // * 連戦しない
)

var piaMessageMark = []byte{0x4C, 0x4B, 0x81, 0x6F}

// * 表のランクの値。攻略サイトの一覧と見比べて決めた。0 は C とボスで、表では区別できないので C として数える
var eventRanks = []struct {
	name  string
	value int
}{{"S", 6}, {"A", 5}, {"B", 4}, {"C", 0}, {"D", 3}, {"E", 2}}

// * event:<PID> か event:<PID>:<ルール> からイベントを取り出す
func eventBotOfMode(mode string) (uint32, bool) {
	rest, isEvent := strings.CutPrefix(mode, "event:")
	if !isEvent {
		return 0, false
	}

	text, _, _ := strings.Cut(rest, ":")
	bot, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		return 0, false
	}

	return uint32(bot), true
}

// * ルールに合わなければ、理由を返す
func eventRuleViolation(body []byte, rules globals.EventRules, tables historyTables) string {
	counts := make(map[int]int)
	names := make(map[string]int)

	for n := 0; n < partyMonCount; n++ {
		record := body[partyRecordSize*n : partyRecordSize*(n+1)]
		species := tables.Species[hexID(record[0x20:0x24])]
		counts[species.Rank]++
		names[species.Name]++

		if rules.Level != nil && int(record[partyLevelOffset]) != *rules.Level {
			return fmt.Sprintf("レベル %d（%d のみ）", record[partyLevelOffset], *rules.Level)
		}
	}

	for _, rank := range eventRanks {
		limit, found := rules.Ranks[rank.name]
		if !found {
			continue
		}

		count := counts[rank.value]
		if limit.Max != nil && count > *limit.Max {
			return fmt.Sprintf("%s ランク %d 体（上限 %d 体）", rank.name, count, *limit.Max)
		}
		if limit.Min != nil && count < *limit.Min {
			return fmt.Sprintf("%s ランク %d 体（下限 %d 体）", rank.name, count, *limit.Min)
		}
	}

	for _, limit := range rules.Species {
		count := names[limit.Name]
		if limit.Max != nil && count > *limit.Max {
			return fmt.Sprintf("%s %d 体（上限 %d 体）", limit.Name, count, *limit.Max)
		}
		if limit.Min != nil && count < *limit.Min {
			return fmt.Sprintf("%s %d 体（必須 %d 体）", limit.Name, count, *limit.Min)
		}
	}

	return ""
}

// * 試験用。参加者の状態の報告で、本人の側の 6 体を全回復に書き換えて署名し直す。書き換えたら true。
// * 相手に届く報告が変わるだけで、本人の本体の中の HP は変わらない
func fakeEventHeal(packet []byte, key []byte) bool {
	end := len(packet) - piaSignatureSize
	if end <= 0 {
		return false
	}

	changed := false
	for at := 0; ; {
		found := bytes.Index(packet[at:end], piaMessageMark)
		if found < 0 {
			break
		}
		at += found + len(piaMessageMark)

		head := at + piaMessageHeadSkip
		body := head + piaMessageHeadSize
		if body+piaStateSize > end {
			continue
		}
		if binary.LittleEndian.Uint16(packet[head+2:]) != piaMessageState || binary.LittleEndian.Uint32(packet[head+8:]) != piaStateSize {
			continue
		}

		if !changed && !hmac.Equal(piaSignature(packet[:end], key), packet[end:]) {
			return false
		}
		for n := 0; n < partyMonCount; n++ {
			entry := body + n*piaStateEntrySize
			copy(packet[entry+piaStateHP:entry+piaStateHP+2], packet[entry+piaStateMaxHP:entry+piaStateMaxHP+2])
		}
		changed = true
	}

	if changed {
		copy(packet[end:], piaSignature(packet[:end], key))
	}
	return changed
}

func (h *battleHistory) fakeHealForTest(pid uint32, packet []byte) {
	if os.Getenv("YW2_DEV_FAKE_HEAL_PID") != strconv.FormatUint(uint64(pid), 10) {
		return
	}

	h.mutex.Lock()
	_, isEvent := eventBotOfMode(h.modes[pid])
	h.mutex.Unlock()
	if !isEvent {
		return
	}

	var key []byte
	err := globals.Postgres.QueryRow(`
		SELECT m.session_key FROM matchmaking.matchmake_sessions m
		WHERE m.id = (SELECT gathering_id FROM tracking.join_gathering WHERE source_pid = $1 ORDER BY date DESC LIMIT 1)
	`, int64(pid)).Scan(&key)
	if err != nil {
		fmt.Printf("[YW2-TEST] pid %d: no session key: %v\n", pid, err)
		return
	}

	if fakeEventHeal(packet, key) {
		fmt.Printf("[YW2-TEST] pid %d: state rewritten to full HP\n", pid)
	}
}

// * 「連戦する」を「連戦しない」に書き換えて、署名をやり直す。書き換えたら true。
// * 署名が合わないパケットには触らない
func declineRematch(packet []byte, key []byte) bool {
	end := len(packet) - piaSignatureSize
	if end <= 0 {
		return false
	}

	changed := false
	for at := 0; ; {
		found := bytes.Index(packet[at:end], piaMessageMark)
		if found < 0 {
			break
		}
		at += found + len(piaMessageMark)

		head := at + piaMessageHeadSkip
		if head+piaMessageHeadSize+4 > end {
			continue
		}
		kind := binary.LittleEndian.Uint16(packet[head+2:])
		size := binary.LittleEndian.Uint32(packet[head+8:])
		payload := binary.LittleEndian.Uint32(packet[head+piaMessageHeadSize:])
		if kind != piaMessageRematch || size != 4 || payload != 0 {
			continue
		}

		// * 署名を確かめてから書き換える
		if !changed && !hmac.Equal(piaSignature(packet[:end], key), packet[end:]) {
			return false
		}
		binary.LittleEndian.PutUint16(packet[head+2:], piaMessageDecline)
		// * 次のバイトは確かめ用で、番号、種類の下位、送信元の XOR。種類に合わせて直す
		packet[head+4] ^= byte(piaMessageRematch ^ piaMessageDecline)
		changed = true
	}

	if changed {
		copy(packet[end:], piaSignature(packet[:end], key))
	}
	return changed
}

func piaSignature(data []byte, key []byte) []byte {
	mac := hmac.New(md5.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// * 書き換える必要があるパケットか、先に安く確かめる
func hasRematchMessage(packet []byte) bool {
	return bytes.Contains(packet, []byte{0x10, 0x10}) && bytes.Contains(packet, piaMessageMark)
}

// * イベントの対戦では、連戦の申し出を「断った」に変える。2 人とも相手に断られたように見える
func (h *battleHistory) rewriteEventPacket(pid uint32, packet []byte) {
	if !hasRematchMessage(packet) || !findRematchMessage(packet) {
		return
	}

	h.mutex.Lock()
	_, isEvent := eventBotOfMode(h.modes[pid])
	h.mutex.Unlock()
	if !isEvent {
		return
	}

	var key []byte
	err := globals.Postgres.QueryRow(`
		SELECT m.session_key FROM matchmaking.matchmake_sessions m
		WHERE m.id = (SELECT gathering_id FROM tracking.join_gathering WHERE source_pid = $1 ORDER BY date DESC LIMIT 1)
	`, int64(pid)).Scan(&key)
	if err != nil {
		fmt.Printf("[YW2-EVENT] pid %d: no session key: %v\n", pid, err)
		return
	}

	now := time.Now()
	h.mutex.Lock()
	first, seen := h.declinedAt[pid]
	if !seen {
		first = now
		h.declinedAt[pid] = now
	}
	h.mutex.Unlock()

	if !declineRematch(packet, key) {
		fmt.Printf("[YW2-EVENT] pid %d: rematch not rewritten, signature differs\n", pid)
		return
	}

	if !seen {
		fmt.Printf("[YW2-EVENT] pid %d: rematch turned into decline\n", pid)
	}

	// * 相手が受け取らないと送り直しが続いて、画面が進まない。そのときは通信を止めて終わらせる
	if now.Sub(first) >= eventDeclineLimit {
		cutEventBattle(pid, eventCutRematch)
		go h.saveCut(pid, eventCutRematch)
	}
}

// * パケットに「連戦する」があるか。鍵がなくても分かる範囲で調べる
func findRematchMessage(packet []byte) bool {
	end := len(packet) - piaSignatureSize
	if end <= 0 {
		return false
	}

	for at := 0; ; {
		found := bytes.Index(packet[at:end], piaMessageMark)
		if found < 0 {
			return false
		}
		at += found + len(piaMessageMark)

		head := at + piaMessageHeadSkip
		if head+piaMessageHeadSize+4 > end {
			continue
		}
		if binary.LittleEndian.Uint16(packet[head+2:]) == piaMessageRematch &&
			binary.LittleEndian.Uint32(packet[head+8:]) == 4 &&
			binary.LittleEndian.Uint32(packet[head+piaMessageHeadSize:]) == 0 {
			return true
		}
	}
}

// * 2 人の間の通信を止める。本体は相手が応答しなくなったと判断して、通信エラーになる
func cutEventBattle(pid uint32, reason string) {
	fmt.Printf("[YW2-EVENT] cut battle of %d: %s\n", pid, reason)

	if activeRelay != nil {
		activeRelay.block(pid)
	}
}
