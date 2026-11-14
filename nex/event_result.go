package nex

// イベントと大会の対戦の勝敗。フレンド対戦の形なので、ゲームは UploadScore を送らない。
// 代わりに、参加者が送る状態の報告（0x1008、12 体の HP）で、片方の 6 体の HP がすべて 0 になったら決着とみなす。
// 前の 6 体が送った本人の側、残りが相手の側（42_イベント対戦の構想.md）。
//
// 報告は参加者しか送らないので、改造した本体なら偽れる。署名（部屋の session_key の HMAC）だけは確かめる。
// 記録は history.battles の state_pid（報告した人）と state_won（報告した人が勝ったか）。UploadScore の score_pid とは混ぜない。

import (
	"bytes"
	"crypto/hmac"
	"encoding/binary"
	"fmt"

	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

// * 報告の中に、どちらかの 6 体が全員倒れたものがあれば、送った本人が勝ったかを返す。署名は見ない
func readEventResult(packet []byte) (won bool, decided bool, end int) {
	end = len(packet) - piaSignatureSize
	if end <= 0 {
		return false, false, end
	}
	for at := 0; ; {
		found := bytes.Index(packet[at:end], piaMessageMark)
		if found < 0 {
			return false, false, end
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

		sideDown := func(first int) bool {
			for n := first; n < first+partyMonCount; n++ {
				entry := body + n*piaStateEntrySize
				maxHP := binary.LittleEndian.Uint16(packet[entry+piaStateMaxHP:])
				hp := binary.LittleEndian.Uint16(packet[entry+piaStateHP:])
				if maxHP == 0 || hp != 0 {
					return false
				}
			}
			return true
		}
		mine, theirs := sideDown(0), sideDown(partyMonCount)
		if mine != theirs {
			return theirs, true, end
		}
	}
}

// * 中継のパケットごとに呼ぶ。イベントの対戦で決着の報告が来たら、1 回だけ記録する
func (h *battleHistory) watchEventResult(pid uint32, packet []byte) {
	won, decided, end := readEventResult(packet)
	if !decided {
		return
	}

	h.mutex.Lock()
	_, isEvent := eventBotOfMode(h.modes[pid])
	done := h.resultSaved[pid]
	if isEvent && !done {
		h.resultSaved[pid] = true
	}
	h.mutex.Unlock()
	if !isEvent || done {
		return
	}

	// * 中継の流れを止めないように、署名の確認と保存は別に
	data := append([]byte(nil), packet...)
	go func() {
		var key []byte
		err := globals.Postgres.QueryRow(`
			SELECT m.session_key FROM matchmaking.matchmake_sessions m
			WHERE m.id = (SELECT gathering_id FROM tracking.join_gathering WHERE source_pid = $1 ORDER BY date DESC LIMIT 1)
		`, int64(pid)).Scan(&key)
		if err != nil {
			fmt.Printf("[YW2-RESULT] pid %d: no session key: %v\n", pid, err)
			h.forgetResult(pid)
			return
		}
		if !hmac.Equal(piaSignature(data[:end], key), data[end:]) {
			fmt.Printf("[YW2-RESULT] pid %d: signature differs, ignored\n", pid)
			h.forgetResult(pid)
			return
		}

		battleID, err := historyBattleID(pid)
		if err != nil {
			fmt.Printf("[YW2-RESULT] pid %d: no battle: %v\n", pid, err)
			return
		}
		_, err = globals.Postgres.Exec(`UPDATE history.battles SET state_pid = $2, state_won = $3, state_at = now() WHERE id = $1 AND state_pid IS NULL`, battleID, int64(pid), won)
		if err != nil {
			fmt.Printf("[YW2-RESULT] battle %d: save failed: %v\n", battleID, err)
			return
		}
		fmt.Printf("[YW2-RESULT] battle %d pid %d won=%v\n", battleID, pid, won)
	}()
}

func (h *battleHistory) forgetResult(pid uint32) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	delete(h.resultSaved, pid)
}
