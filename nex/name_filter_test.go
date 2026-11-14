package nex

import (
	"encoding/base64"
	"testing"
)

// * テスト用の Shift-JIS（使う文字だけ）
var testSJIS = map[rune][]byte{
	'Ｅ': {0x82, 0x64}, 'ｅ': {0x82, 0x85}, '：': {0x81, 0x46}, '杯': {0x94, 0x74},
	'認': {0x94, 0x46}, '証': {0x8F, 0xD8}, '■': {0x81, 0xA1}, 'あ': {0x82, 0xA0},
}

func toSJIS(s string) []byte {
	out := []byte{}
	for _, r := range s {
		if b, ok := testSJIS[r]; ok {
			out = append(out, b...)
		} else {
			out = append(out, byte(r))
		}
	}
	return out
}

func TestFilterSJISName(t *testing.T) {
	cases := []struct {
		in, want string
		changed  bool
	}{
		{"E:abc", "■■abc", true},
		{"Ｅ：各", "■■各", true},
		{"e:x", "■■x", true},
		{"ｅ:x", "■■x", true},
		{"杯：あ", "■■あ", true},
		{"杯:あ", "■■あ", true},
		{"杯あ", "杯あ", false},
		{"認証:ABC", "■■■ABC", true},
		{"認証：ABC", "■■■ABC", true},
		{"認証あ", "■■あ", true},
		{"あE:あ", "あ■■あ", true},
		{"Eあ", "Eあ", false},
		{"ふつう", "ふつう", false},
	}
	for _, c := range cases {
		got, changed := filterSJISName(toSJIS(c.in), 27)
		if string(got) != string(toSJIS(c.want)) || changed != c.changed {
			t.Errorf("%q: got %x changed=%v, want %x changed=%v", c.in, got, changed, toSJIS(c.want), c.changed)
		}
	}
}

func TestFilterSJISNameKeepsLimit(t *testing.T) {
	// * E: を 9 個（18 バイト）→ ■ が 18 個（36 バイト）になるので、27 バイト以内に文字の区切りで切る
	in := []byte{}
	for i := 0; i < 9; i++ {
		in = append(in, 'E', ':')
	}
	got, changed := filterSJISName(in, 27)
	if !changed || len(got) != 26 || len(got)%2 != 0 {
		t.Errorf("got %d bytes changed=%v", len(got), changed)
	}
}

func TestFilterNotificationName(t *testing.T) {
	field := make([]byte, 40)
	copy(field, toSJIS("認証:ABC"))
	field[30] = 0x7F // * 名前より後ろはそのまま残る
	out, changed := filterNotificationName(base64.StdEncoding.EncodeToString(field))
	raw, _ := base64.StdEncoding.DecodeString(out)
	if !changed || len(raw) != 40 || raw[30] != 0x7F || string(raw[:9]) != string(toSJIS("■■■ABC")) || raw[9] != 0 {
		t.Errorf("got %x changed=%v", raw, changed)
	}
	// * プレイヤーの通知: 名前（16 バイト）、0 が 4 バイト、そのあとに別のデータ（壊さない）
	player := make([]byte, 28)
	copy(player, toSJIS("E:あああああああ"))
	copy(player[20:], []byte{0xC0, 0x58, 0xBD, 0x08, 0x83, 0x3F, 0x8A, 0xFE})
	out, changed = filterNotificationName(base64.StdEncoding.EncodeToString(player))
	raw, _ = base64.StdEncoding.DecodeString(out)
	if !changed || string(raw[20:]) != string(player[20:]) || raw[19] != 0 || len(raw) != 28 {
		t.Errorf("player data broken: %x changed=%v", raw, changed)
	}
	if string(raw[:18]) != string(toSJIS("■■あああああああ")) {
		t.Errorf("player name: %x", raw[:20])
	}
	if _, changed := filterNotificationName("not base64!!"); changed {
		t.Error("base64 でないものを変えた")
	}
}
