package nex

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

// * 紐付けの bot（YW2_LINK_BOT）: Discord と PID の紐付けに使う。
// * この bot の名前は、見に来た人ごとに「認証:」+ 英数字になる（event_link_notify.go）。
// * 誘うと断り、少しあとにその名前で誘い返す。英数字は、誘いへの返事か時間切れで無効になる
var linkTestBot = func() uint32 {
	value, _ := strconv.ParseUint(strings.TrimSpace(os.Getenv("YW2_LINK_BOT")), 10, 32)
	return uint32(value)
}()

const (
	linkCodeLetters = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // * 見間違えやすい 0 O 1 I は使わない
	linkCodeLength  = 11                                 // * 画面に収まる長さ。「認証:」と合わせて 14 文字（E の名前と同じ）。名前の欄は 27 バイトまで
	linkCodeLife    = 3 * time.Minute
)

var (
	linkMutex sync.Mutex
	linkCodes = make(map[uint32]linkCode) // * PID ごとに、待っている誘いの英数字
)

type linkCode struct {
	code    string
	expires time.Time
}

func newLinkCode() string {
	random := make([]byte, linkCodeLength)
	if _, err := rand.Read(random); err != nil {
		return ""
	}
	code := make([]byte, linkCodeLength)
	for n, b := range random {
		code[n] = linkCodeLetters[int(b)%len(linkCodeLetters)]
	}
	return string(code)
}

// * その人の英数字（なければ作る）。名前を見に来たときと、誘われたときに使う
func linkCodeFor(pid uint32) string {
	linkMutex.Lock()
	defer linkMutex.Unlock()
	if entry, ok := linkCodes[pid]; ok && time.Now().Before(entry.expires) {
		return entry.code
	}
	code := newLinkCode()
	linkCodes[pid] = linkCode{code: code, expires: time.Now().Add(linkCodeLife)}
	fmt.Printf("[YW2-LINK] pid=%d sees code %s\n", pid, code)
	return code
}

// * 誘い返した: 英数字を DB に置く（返事か時間切れで消す）。Discord の bot は、ここにある英数字だけを受け付ける
func setLinkCode(pid uint32, code string) {
	expires := time.Now().Add(linkCodeLife)
	linkMutex.Lock()
	linkCodes[pid] = linkCode{code: code, expires: expires}
	linkMutex.Unlock()

	if globals.Postgres == nil || !ensureLinkTables() {
		return
	}
	if _, err := globals.Postgres.Exec(`INSERT INTO history.link_codes (code, pid, expires) VALUES ($1, $2, $3)
		ON CONFLICT (code) DO UPDATE SET pid = EXCLUDED.pid, expires = EXCLUDED.expires`, code, pid, expires); err != nil {
		fmt.Printf("[YW2-LINK] cannot save code: %v\n", err)
	}
}

var (
	linkTablesOnce sync.Once
	linkTablesOK   bool
)

// * 紐付けの表。英数字（待っている間だけ）と、Discord と PID の結び付き
func ensureLinkTables() bool {
	linkTablesOnce.Do(func() {
		_, err := globals.Postgres.Exec(`
			CREATE SCHEMA IF NOT EXISTS history;
			CREATE TABLE IF NOT EXISTS history.link_codes (
				code text PRIMARY KEY,
				pid bigint NOT NULL,
				expires timestamptz NOT NULL
			);
			CREATE TABLE IF NOT EXISTS history.discord_links (
				pid bigint PRIMARY KEY,
				discord_id text NOT NULL,
				discord_name text NOT NULL,
				linked_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX IF NOT EXISTS discord_links_discord_id ON history.discord_links (discord_id);
			DELETE FROM history.link_codes;`)
		if err != nil {
			fmt.Printf("[YW2-LINK] cannot create tables: %v\n", err)
			return
		}
		linkTablesOK = true
	})
	return linkTablesOK
}

// * 誘いに返事があった（はい・いいえ）か、時間切れ。英数字を無効にする
func dropLinkCode(pid uint32, why string) {
	linkMutex.Lock()
	defer linkMutex.Unlock()
	if entry, ok := linkCodes[pid]; ok {
		delete(linkCodes, pid)
		fmt.Printf("[YW2-LINK] pid=%d code %s dropped (%s)\n", pid, entry.code, why)
		if globals.Postgres != nil && ensureLinkTables() {
			if _, err := globals.Postgres.Exec(`DELETE FROM history.link_codes WHERE code = $1`, entry.code); err != nil {
				fmt.Printf("[YW2-LINK] cannot drop code: %v\n", err)
			}
		}
	}
}
