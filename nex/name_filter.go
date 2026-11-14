package nex

// フレンド一覧の名前で、bot になりすませないようにする（11-14、ユーザーの希望）。
// bot 以外のプレイヤーの名前にある次の文字列を ■ に置き換えてから、ゲームへ送る。半角と全角は同じとして扱う。
//   E: → ■■（E は Ｅ・ｅ・e も、: は ： も同じ）
//   杯: → ■■
//   認証: → ■■■
//   認証 → ■■
// 名前は Shift-JIS で、通知の文字列（base64）の先頭 28 バイト（0 で終わる）。DB の中身は変えず、送るときだけ置き換える。

import (
	"bytes"
	"encoding/base64"
)

const notificationNameSize = 28

var (
	sjisBlock = []byte{0x81, 0xA1} // * ■
	sjisColon = [][]byte{{':'}, {0x81, 0x46}}
	sjisE     = [][]byte{{'E'}, {'e'}, {0x82, 0x64}, {0x82, 0x85}}
	sjisCup   = []byte{0x94, 0x74}             // * 杯
	sjisAuth  = []byte{0x94, 0x46, 0x8F, 0xD8} // * 認証
)

// * Shift-JIS の 1 文字の長さ
func sjisCharLen(b []byte) int {
	if len(b) >= 2 && ((b[0] >= 0x81 && b[0] <= 0x9F) || (b[0] >= 0xE0 && b[0] <= 0xFC)) {
		return 2
	}
	return 1
}

// * b の先頭が、choices のどれかの 1 文字なら、その長さ
func sjisStartsWithAny(b []byte, choices [][]byte) int {
	for _, c := range choices {
		if bytes.HasPrefix(b, c) {
			return len(c)
		}
	}
	return 0
}

// * 名前（0 の前まで）の置き換え。結果は max バイトまで（文字の途中では切らない）
func filterSJISName(name []byte, max int) ([]byte, bool) {
	out := make([]byte, 0, len(name)+8)
	changed := false
	block := func(n int) {
		for i := 0; i < n; i++ {
			out = append(out, sjisBlock...)
		}
		changed = true
	}
	for i := 0; i < len(name); {
		rest := name[i:]
		if bytes.HasPrefix(rest, sjisAuth) {
			if n := sjisStartsWithAny(rest[len(sjisAuth):], sjisColon); n > 0 {
				block(3)
				i += len(sjisAuth) + n
			} else {
				block(2)
				i += len(sjisAuth)
			}
			continue
		}
		if bytes.HasPrefix(rest, sjisCup) {
			if n := sjisStartsWithAny(rest[len(sjisCup):], sjisColon); n > 0 {
				block(2)
				i += len(sjisCup) + n
				continue
			}
		}
		if e := sjisStartsWithAny(rest, sjisE); e > 0 {
			if n := sjisStartsWithAny(rest[e:], sjisColon); n > 0 {
				block(2)
				i += e + n
				continue
			}
		}
		size := sjisCharLen(rest)
		out = append(out, rest[:size]...)
		i += size
	}
	if !changed {
		return name, false
	}
	// * 長くなったら、文字の途中で切らないように詰める
	if len(out) > max {
		cut := 0
		for cut < len(out) {
			size := sjisCharLen(out[cut:])
			if cut+size > max {
				break
			}
			cut += size
		}
		out = out[:cut]
	}
	return out, true
}

// * 通知の文字列（base64）の名前の部分を置き換える。置き換えたら新しい文字列と true
func filterNotificationName(param string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(param)
	if err != nil || len(raw) == 0 {
		return param, false
	}
	area := min(len(raw), notificationNameSize)
	end := bytes.IndexByte(raw[:area], 0)
	if end < 0 {
		return param, false
	}
	// * 名前のあとの 0 の続く所までが使える。その先（プレイヤーの通知では名前のあとに別のデータがある）は変えない
	padEnd := end
	for padEnd < area && raw[padEnd] == 0 {
		padEnd++
	}
	filtered, changed := filterSJISName(raw[:end], padEnd-1)
	if !changed {
		return param, false
	}
	result := append([]byte(nil), raw...)
	copy(result, filtered)
	for i := len(filtered); i < padEnd; i++ {
		result[i] = 0
	}
	return base64.StdEncoding.EncodeToString(result), true
}
