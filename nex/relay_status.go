package nex

// 中継のポートの使い方（56_同時対戦の見積もり.md）。1 分ごとに history.relay_status（1 行だけ）へ書く。
// 管理画面の「サーバー」が読む。8 割を超えたらログに警告を出す。数はゲームのサーバーを起動してからのもの。

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

var (
	relayExhausted   atomic.Int64 // * ポートが足りず、中継できなかった回数
	relayPeak        int
	relayStartedAt   = time.Now()
	relayStatusOnce  sync.Once
	relayStatusReady bool
)

func (relay *packetRelay) usage() (used int, total int) {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()
	used = len(relay.byPID)
	return used, used + len(relay.freePorts)
}

func (relay *packetRelay) saveStatus() {
	used, total := relay.usage()
	if used > relayPeak {
		relayPeak = used
	}
	if total > 0 && used*10 >= total*8 {
		fmt.Printf("[YW2-RELAY] warning: %d of %d relay ports in use\n", used, total)
	}
	if globals.Postgres == nil {
		return
	}
	relayStatusOnce.Do(func() {
		_, err := globals.Postgres.Exec(`
			CREATE TABLE IF NOT EXISTS history.relay_status (
				id integer PRIMARY KEY,
				used integer NOT NULL,
				total integer NOT NULL,
				peak integer NOT NULL,
				exhausted bigint NOT NULL,
				started_at timestamptz NOT NULL,
				at timestamptz NOT NULL
			)`)
		if err != nil {
			fmt.Printf("[YW2-RELAY] cannot create status table: %v\n", err)
			return
		}
		relayStatusReady = true
	})
	if !relayStatusReady {
		return
	}
	_, err := globals.Postgres.Exec(`
		INSERT INTO history.relay_status (id, used, total, peak, exhausted, started_at, at) VALUES (1, $1, $2, $3, $4, $5, now())
		ON CONFLICT (id) DO UPDATE SET used = EXCLUDED.used, total = EXCLUDED.total, peak = EXCLUDED.peak,
			exhausted = EXCLUDED.exhausted, started_at = EXCLUDED.started_at, at = EXCLUDED.at`,
		used, total, relayPeak, relayExhausted.Load(), relayStartedAt)
	if err != nil {
		fmt.Printf("[YW2-RELAY] cannot save status: %v\n", err)
	}
}
