package nex

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

func intPointer(value int) *int {
	return &value
}

// * ランクと、レベルの一覧からパーティを作る
func eventTestParty(ranks []int, levels []byte) ([]byte, historyTables) {
	tables := historyTables{Species: map[string]historyEntry{}}
	body := make([]byte, partyRecordSize*partyMonCount)

	for n := range ranks {
		record := body[partyRecordSize*n:]
		record[0x20] = byte(n + 1)
		record[partyLevelOffset] = levels[n]
		tables.Species[hexID(record[0x20:0x24])] = historyEntry{Rank: ranks[n]}
	}

	return body, tables
}

func TestEventRuleViolation(t *testing.T) {
	levels := []byte{60, 60, 60, 60, 60, 99}
	body, tables := eventTestParty([]int{6, 6, 6, 5, 4, 0}, levels)

	cases := []struct {
		name  string
		rules globals.EventRules
		want  string
	}{
		{"no rules", globals.EventRules{}, ""},
		{"S within", globals.EventRules{Ranks: map[string]globals.EventRankLimit{"S": {Max: intPointer(3)}}}, ""},
		{"S over", globals.EventRules{Ranks: map[string]globals.EventRankLimit{"S": {Max: intPointer(2)}}}, "S ランク 3 体（上限 2 体）"},
		{"S min ok", globals.EventRules{Ranks: map[string]globals.EventRankLimit{"S": {Min: intPointer(3)}}}, ""},
		{"S min under", globals.EventRules{Ranks: map[string]globals.EventRankLimit{"S": {Min: intPointer(4)}}}, "S ランク 3 体（下限 4 体）"},
		{"D forbidden and none", globals.EventRules{Ranks: map[string]globals.EventRankLimit{"D": {Max: intPointer(0)}}}, ""},
		{"E required", globals.EventRules{Ranks: map[string]globals.EventRankLimit{"E": {Min: intPointer(1)}}}, "E ランク 0 体（下限 1 体）"},
		{"C forbidden", globals.EventRules{Ranks: map[string]globals.EventRankLimit{"C": {Max: intPointer(0)}}}, "C ランク 1 体（上限 0 体）"},
		{"level differs", globals.EventRules{Level: intPointer(60)}, "レベル 99（60 のみ）"},
	}

	for _, test := range cases {
		if got := eventRuleViolation(body, test.rules, tables); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
}

// * 「各ランク 1 体、レベル 60 だけ」
func TestEventRuleOneOfEachRank(t *testing.T) {
	one := globals.EventRankLimit{Min: intPointer(1), Max: intPointer(1)}
	rules := globals.EventRules{
		Ranks: map[string]globals.EventRankLimit{"S": one, "A": one, "B": one, "C": one, "D": one, "E": one},
		Level: intPointer(60),
	}
	levels := []byte{60, 60, 60, 60, 60, 60}

	body, tables := eventTestParty([]int{6, 5, 4, 0, 3, 2}, levels)
	if got := eventRuleViolation(body, rules, tables); got != "" {
		t.Fatalf("legal: %s", got)
	}

	body, tables = eventTestParty([]int{6, 6, 4, 0, 3, 2}, levels)
	if got := eventRuleViolation(body, rules, tables); got != "S ランク 2 体（上限 1 体）" {
		t.Fatalf("two S: %s", got)
	}

	// * 上にも下にも外れたらだめ
	body, tables = eventTestParty([]int{6, 5, 4, 0, 3, 2}, []byte{60, 60, 60, 60, 60, 59})
	if got := eventRuleViolation(body, rules, tables); got != "レベル 59（60 のみ）" {
		t.Fatalf("low level: %s", got)
	}
	body, tables = eventTestParty([]int{6, 5, 4, 0, 3, 2}, []byte{61, 60, 60, 60, 60, 60})
	if got := eventRuleViolation(body, rules, tables); got != "レベル 61（60 のみ）" {
		t.Fatalf("high level: %s", got)
	}
}

func TestEventRuleSpecies(t *testing.T) {
	body, tables := eventTestParty([]int{6, 5, 4, 0, 3, 2}, []byte{60, 60, 60, 60, 60, 60})

	// * 別の ID でも、同じ名前なら同じ妖怪として数える
	names := []string{"ジバニャン", "ジバニャン", "コマさん", "ロボニャン", "雨女", "ムリカベ"}
	n := 0
	for id := range tables.Species {
		entry := tables.Species[id]
		entry.Name = names[n]
		tables.Species[id] = entry
		n++
	}

	cases := []struct {
		name  string
		limit globals.EventSpeciesLimit
		want  string
	}{
		{"within", globals.EventSpeciesLimit{Name: "ジバニャン", Max: intPointer(2)}, ""},
		{"over", globals.EventSpeciesLimit{Name: "ジバニャン", Max: intPointer(1)}, "ジバニャン 2 体（上限 1 体）"},
		{"banned", globals.EventSpeciesLimit{Name: "雨女", Max: intPointer(0)}, "雨女 1 体（上限 0 体）"},
		{"required ok", globals.EventSpeciesLimit{Name: "コマさん", Min: intPointer(1)}, ""},
		{"required missing", globals.EventSpeciesLimit{Name: "キュウビ", Min: intPointer(1)}, "キュウビ 0 体（必須 1 体）"},
		{"not in party", globals.EventSpeciesLimit{Name: "キュウビ", Max: intPointer(0)}, ""},
	}

	for _, test := range cases {
		rules := globals.EventRules{Species: []globals.EventSpeciesLimit{test.limit}}
		if got := eventRuleViolation(body, rules, tables); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
}

func TestBlockedPartySavedOnce(t *testing.T) {
	body, tables := eventTestParty([]int{6, 5, 4, 0, 3, 2}, []byte{60, 60, 60, 60, 60, 60})
	h := &battleHistory{tables: tables, last: make(map[uint32][]byte), partyAt: make(map[uint32]time.Time)}

	packet := append([]byte{1, 2, 3}, partyLengthMark...)
	packet = append(packet, make([]byte, partyHeaderSkip-len(partyLengthMark))...)
	packet = append(packet, body...)

	if h.blockedParty(1, []byte("not a party")) != nil {
		t.Fatal("saved a packet without a party")
	}
	if got := h.blockedParty(1, packet); !bytes.Equal(got, body) {
		t.Fatalf("first: %x", got)
	}
	if h.blockedParty(1, packet) != nil {
		t.Fatal("saved twice")
	}

	// * 止める前に届いていた人（連戦）は、1 戦目の記録を残す
	h.partyAt[2] = time.Now()
	if h.blockedParty(2, packet) != nil {
		t.Fatal("overwrote the first battle")
	}
}

// * 実際の通信と同じ形のパケットを作る
func piaTestPacket(key []byte, kind uint16, size uint32, payload uint32) []byte {
	packet := []byte{0x32, 0xAB, 0x98, 0x64, 0x01, 0x57, 0x00, 0xEC, 0x14, 0x06, 0xE6, 0xFC}
	packet = append(packet, make([]byte, 20)...)
	packet = append(packet, piaMessageMark...)
	// * 番号 0x36、送信元 1。確かめ用のバイトは 3 つの XOR
	packet = append(packet, 0x36, 0, 0, 0x44, 0x12, 0, 0, 0)
	packet = append(packet, 0x01, 0x00, byte(kind), byte(kind>>8), 0x36^byte(kind)^0x01, 0x10, 0x00, 0x44)
	packet = append(packet, byte(size), byte(size>>8), byte(size>>16), byte(size>>24))
	packet = append(packet, byte(payload), byte(payload>>8), byte(payload>>16), byte(payload>>24))
	packet = append(packet, piaMessageMark...)
	return append(packet, piaSignature(packet, key)...)
}

func TestDeclineRematch(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	packet := piaTestPacket(key, piaMessageRematch, 4, 0)
	if !findRematchMessage(packet) {
		t.Fatal("not found")
	}
	if !declineRematch(packet, key) {
		t.Fatal("not rewritten")
	}
	want := piaTestPacket(key, piaMessageDecline, 4, 0)
	if !bytes.Equal(packet, want) {
		t.Fatalf("got %x, want %x", packet, want)
	}

	// * 実際の通信で見えた値: 番号 0x36 の「連戦する」が 0x27、番号 0x64 の「連戦しない」が 0x74
	if rematch := piaTestPacket(key, piaMessageRematch, 4, 0); rematch[bytes.Index(rematch, piaMessageMark)+len(piaMessageMark)+piaMessageHeadSkip+4] != 0x27 {
		t.Fatal("check byte of rematch")
	}
	if byte(0x64^0x11^0x01) != 0x74 {
		t.Fatal("check byte of decline")
	}

	// * もう書き換えるものはない
	if findRematchMessage(packet) || declineRematch(packet, key) {
		t.Fatal("rewritten twice")
	}

	// * 別の鍵の署名なら触らない
	other := piaTestPacket(key, piaMessageRematch, 4, 0)
	before := append([]byte(nil), other...)
	if declineRematch(other, []byte("another key")) || !bytes.Equal(other, before) {
		t.Fatal("rewrote a packet with a bad signature")
	}

	// * 別の種類、形の違うものは触らない
	for _, test := range [][]byte{
		piaTestPacket(key, 0x1001, 4, 0),
		piaTestPacket(key, piaMessageDecline, 4, 0),
		piaTestPacket(key, piaMessageRematch, 8, 0),
		piaTestPacket(key, piaMessageRematch, 4, 1),
		{1, 2, 3},
	} {
		if findRematchMessage(test) || declineRematch(test, key) {
			t.Fatalf("touched %x", test)
		}
	}
}

func TestFakeEventHeal(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	build := func(hp uint16) []byte {
		state := make([]byte, piaStateSize)
		for n := 0; n < 12; n++ {
			entry := state[n*piaStateEntrySize:]
			binary.LittleEndian.PutUint16(entry, 99)
			binary.LittleEndian.PutUint16(entry[piaStateMaxHP:], 300)
			current := hp
			if n >= partyMonCount {
				current = 7 // * 相手の側は変えない
			}
			binary.LittleEndian.PutUint16(entry[piaStateHP:], current)
		}
		packet := []byte{0x32, 0xAB, 0x98, 0x64, 0x01, 0x57, 0x00, 0xEC, 0x14, 0x06, 0xE6, 0xFC}
		packet = append(packet, piaMessageMark...)
		packet = append(packet, 0x36, 0, 0, 0x44, 0x12, 0, 0, 0)
		packet = append(packet, 0x01, 0x00, 0x08, 0x10, 0x36^0x08^0x01, 0x10, 0x00, 0x44)
		packet = binary.LittleEndian.AppendUint32(packet, piaStateSize)
		packet = append(packet, state...)
		packet = append(packet, piaMessageMark...)
		return append(packet, piaSignature(packet, key)...)
	}

	packet := build(12)
	if !fakeEventHeal(packet, key) {
		t.Fatal("not rewritten")
	}
	if want := build(300); !bytes.Equal(packet, want) {
		t.Fatalf("got %x, want %x", packet, want)
	}

	// * 別の鍵の署名なら触らない
	other := build(12)
	before := append([]byte(nil), other...)
	if fakeEventHeal(other, []byte("another key")) || !bytes.Equal(other, before) {
		t.Fatal("rewrote a packet with a bad signature")
	}

	// * 別のメッセージは触らない
	rematch := piaTestPacket(key, piaMessageRematch, 4, 0)
	if fakeEventHeal(rematch, key) {
		t.Fatal("touched another message")
	}
}

func TestEventBotOfMode(t *testing.T) {
	cases := []struct {
		mode string
		bot  uint32
		ok   bool
	}{
		{"event:1999999001", 1999999001, true},
		{"event:1999999001:1", 1999999001, true},
		{"friend", 0, false},
		{"random", 0, false},
		{"", 0, false},
		{"event:", 0, false},
	}

	for _, test := range cases {
		if bot, ok := eventBotOfMode(test.mode); bot != test.bot || ok != test.ok {
			t.Errorf("%q: %d %v", test.mode, bot, ok)
		}
	}
}

func TestRelayBlockStopsBothWays(t *testing.T) {
	relay := newPacketRelay("127.0.0.1", relayTestFirstPort+60, 4)
	one, two := testClient(t), testClient(t)
	s1, err := relay.assign(1, localAddr(one.LocalAddr().(*net.UDPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := relay.assign(2, localAddr(two.LocalAddr().(*net.UDPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}

	one.WriteToUDP([]byte("hello"), relayAddr(s2.port))
	if data, _ := readPacket(t, two); data != "hello" {
		t.Fatalf("before: %s", data)
	}

	// * 片方を止めると、相手の分も止まる
	relay.block(1)
	two.WriteToUDP([]byte("stop"), relayAddr(s1.port))
	expectNoPacket(t, one)
	one.WriteToUDP([]byte("stop"), relayAddr(s2.port))
	expectNoPacket(t, two)

	// * 新しい部屋に入るときに戻す。相手の分は相手が戻す
	relay.unblock(1)
	two.WriteToUDP([]byte("still"), relayAddr(s1.port))
	expectNoPacket(t, one)
	relay.unblock(2)
	two.WriteToUDP([]byte("again"), relayAddr(s1.port))
	if data, _ := readPacket(t, one); data != "again" {
		t.Fatalf("after: %s", data)
	}
}
