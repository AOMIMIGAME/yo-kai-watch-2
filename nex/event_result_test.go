package nex

import (
	"encoding/binary"
	"testing"
)

// * 状態の報告 1 つだけのパケット。hp は 12 体の今の HP（最大 HP はどれも 100）
func testStatePacket(hp [12]uint16) []byte {
	packet := append([]byte{}, piaMessageMark...)
	packet = append(packet, make([]byte, piaMessageHeadSkip)...)
	head := make([]byte, piaMessageHeadSize)
	binary.LittleEndian.PutUint16(head[2:], piaMessageState)
	binary.LittleEndian.PutUint32(head[8:], piaStateSize)
	packet = append(packet, head...)
	body := make([]byte, piaStateSize)
	for n, value := range hp {
		entry := n * piaStateEntrySize
		binary.LittleEndian.PutUint16(body[entry+piaStateMaxHP:], 100)
		binary.LittleEndian.PutUint16(body[entry+piaStateHP:], value)
	}
	packet = append(packet, body...)
	return append(packet, make([]byte, piaSignatureSize)...)
}

func TestReadEventResult(t *testing.T) {
	cases := []struct {
		name    string
		hp      [12]uint16
		won     bool
		decided bool
	}{
		{"続いている", [12]uint16{0, 0, 3, 40, 0, 0, 50, 0, 0, 0, 0, 0}, false, false},
		{"相手が全滅", [12]uint16{10, 0, 0, 0, 0, 5, 0, 0, 0, 0, 0, 0}, true, true},
		{"自分が全滅", [12]uint16{0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0}, false, true},
		{"両方 0 は決めない", [12]uint16{}, false, false},
	}
	for _, c := range cases {
		won, decided, _ := readEventResult(testStatePacket(c.hp))
		if won != c.won || decided != c.decided {
			t.Errorf("%s: won=%v decided=%v、期待は won=%v decided=%v", c.name, won, decided, c.won, c.decided)
		}
	}

	if _, decided, _ := readEventResult([]byte{1, 2, 3}); decided {
		t.Error("短いパケットで決着にした")
	}
}
