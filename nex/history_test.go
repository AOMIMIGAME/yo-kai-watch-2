package nex

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// * 実際の記録と QR の正解で確かめる。ファイルがない環境では飛ばす
func TestEncodePartyCodeMatchesQR(t *testing.T) {
	tables, err := loadHistoryTables(os.Getenv("YW2_TEST_TABLES"))
	if err != nil {
		t.Skip("no tables: ", err)
	}

	file, err := os.Open(os.Getenv("YW2_TEST_RELAY"))
	if err != nil {
		t.Skip("no capture: ", err)
	}
	defer file.Close()

	want, _ := hex.DecodeString("59500100c2f800200000280000001400002800188a4e05200a00001e00000014004001298e650710000028000000140000280018c25807201900000a0000001400400118c25407201900000a0000001400400118c24802201900000a00000014004001182252")

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	found := 0

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		data, _ := hex.DecodeString(fields[5])

		body := findPartyBody(data, tables)
		if body == nil {
			continue
		}

		found++
		code, reason := encodePartyCode(body, tables)
		if reason != "" {
			t.Fatalf("pid %s: %s", fields[1], reason)
		}

		if fields[1] == os.Getenv("YW2_TEST_PID") && !bytes.Equal(code, want) {
			t.Fatalf("phone code differs:\n%x\n%x", code, want)
		}
	}

	if found != 2 {
		t.Fatalf("expected 2 party packets, found %d", found)
	}
}

func TestNotificationName(t *testing.T) {
	// * 実際の呼び出し（あおみみがめ）
	parameters, err := hex.DecodeString("6c00000002000000264a0a1729006771434371494c6467743243716f4c6641414141414d424976516a34533730494870376a2f773d3d00")
	if err != nil {
		t.Fatal(err)
	}

	want := []byte{0x82, 0xa0, 0x82, 0xa8, 0x82, 0xdd, 0x82, 0xdd, 0x82, 0xaa, 0x82, 0xdf}
	if name := notificationName(parameters); !bytes.Equal(name, want) {
		t.Fatalf("%x", name)
	}

	if name := notificationName(parameters[:20]); name != nil {
		t.Fatalf("short: %x", name)
	}
}

func TestAppBufferName(t *testing.T) {
	parameters := append([]byte{1, 2, 3}, appBufferMark...)
	parameters = append(parameters, 0x82, 0xA0, 0x82, 0xA8)
	parameters = append(parameters, make([]byte, 40)...)

	if got := appBufferName(parameters); !bytes.Equal(got, []byte{0x82, 0xA0, 0x82, 0xA8}) {
		t.Fatalf("got %x", got)
	}
}

func TestGymCountsRejectsOutOfRange(t *testing.T) {
	if _, ok := gymCounts([]int{0, 0, 25, 0, -10}); !ok {
		t.Fatal("valid course rejected")
	}
	if _, ok := gymCounts([]int{0, 0, 26, 0, -10}); ok {
		t.Fatal("invalid value accepted")
	}
}
