package nex

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	common_globals "github.com/PretendoNetwork/nex-protocols-common-go/v2/globals"
	messaging_constants "github.com/PretendoNetwork/nex-protocols-go/v2/messaging/constants"
	messaging_types "github.com/PretendoNetwork/nex-protocols-go/v2/messaging/types"
	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

// * 誘いの本文は 8 バイト。先頭がルール（1 無制限、2 レベル 60 固定）、6 バイト目が種類
const (
	eventMessageInvite  = 0
	eventMessageCancel  = 1
	eventMessageDecline = 2

	eventInviteDelay      = 2 * time.Second        // * 待機が終わって画面が戻るまで待つ
	eventOfferLimit       = 60 * time.Second       // * 申し込みへの返事を待つ時間。フィールドに戻るまでの時間も含む
	eventRuleDeclineDelay = 500 * time.Millisecond // * 違うルールの人への断り。正しい流れの断り（2 秒）より早く返す
	eventHoldLimit        = 25 * time.Minute       // * 部屋で待つ人をここで帰す。本体は 19 分以上待てた（46_部屋主の交代の計画.md）。部屋の記録は 30 分で消える
	eventSyncEvery        = 5 * time.Second        // * 管理画面の開催と停止を読む間隔

	// * イベントはフリー・レベルそのままで戦う。ルールの中身はサーバーがパーティを見て確かめる
	eventRequiredRule = 1
)

func parseEventInvite(body []byte) (rule uint32, kind byte, ok bool) {
	if len(body) != 8 {
		return 0, 0, false
	}

	rule = uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16 | uint32(body[3])<<24

	return rule, body[5], true
}

func eventInviteBody(body []byte, kind byte) []byte {
	result := append([]byte(nil), body...)
	result[5] = kind

	return result
}

// * 偽のプレイヤーを誘った人どうしだけを組ませる（46_部屋主の交代の計画.md）。
// * 1 人目は断らずに、自分の部屋（誘ったときに本体が作る部屋）で待ってもらう。部屋の持ち主になる。
// * 次の人が誘ったら、その人の誘いは断ってフィールドに戻し、すぐ 1 人目の部屋への申し込みを出す
func registerEventInvites() {
	if len(globals.EventBots()) == 0 {
		return
	}

	// * 開催中のイベントだけを一覧に出す。
	// * サーバーの起動時に全員分がオフライン扱いになるので、そのときも開催中のものは戻る
	go func() {
		for {
			syncEventSettings()
			time.Sleep(eventSyncEvery)
		}
	}()

	globals.EventRoomAlive = func(pid uint32) bool {
		return globals.MessagingManager.Endpoint.FindConnectionByPID(uint64(pid)) != nil
	}

	original := globals.MessagingManager.ProcessMessage

	// * 偽のプレイヤーから本人へ送る
	send := func(manager *common_globals.MessagingManager, message types.DataHolder, binaryMessage messaging_types.BinaryMessage, header messaging_types.UserMessage, bot uint32, target uint32, kind byte, sendMessage bool) {
		binaryMessage.BinaryBody = types.QBuffer(eventInviteBody(binaryMessage.BinaryBody, kind))
		message.Object = binaryMessage

		header.PIDSender = types.PID(bot)
		header.StrSender = types.String(strconv.FormatUint(uint64(bot), 10))

		forwarded, nexError := manager.SetMessageHeader(message, header)
		if nexError != nil {
			common_globals.Logger.Error(nexError.Error())
			return
		}

		_, _, _, nexError = original(manager, forwarded, types.List[types.UInt64]{types.UInt64(target)}, messaging_constants.RecipientType(1), sendMessage)
		if nexError != nil {
			common_globals.Logger.Error(nexError.Error())
		}
	}

	globals.MessagingManager.ProcessMessage = func(manager *common_globals.MessagingManager, message types.DataHolder, recipientIDs types.List[types.UInt64], recipientType messaging_constants.RecipientType, sendMessage bool) (types.DataHolder, types.List[types.UInt32], types.List[types.PID], *nex.Error) {
		if recipientType != 1 || len(recipientIDs) != 1 || !globals.IsEventBot(uint32(recipientIDs[0])) {
			return original(manager, message, recipientIDs, recipientType, sendMessage)
		}

		bot := uint32(recipientIDs[0])

		header, nexError := manager.GetMessageHeader(message)
		if nexError != nil {
			return message, nil, nil, nexError
		}

		player := uint32(header.PIDSender)

		binaryMessage, isBinary := message.Object.(messaging_types.BinaryMessage)
		if !isBinary {
			return message, nil, nil, nil
		}

		rule, kind, ok := parseEventInvite(binaryMessage.BinaryBody)
		if !ok {
			fmt.Printf("[YW2-EVENT] pid=%d sent unknown message to %d: %x\n", player, bot, []byte(binaryMessage.BinaryBody))
			return message, nil, nil, nil
		}
		noteEventMessage(player, bot, kind, binaryMessage.BinaryBody)

		// * 試験用: サーバーが持ち主を演じる部屋を用意して、本人に申し込む
		if host := vhostOfInvite(player, bot); host != nil && kind == eventMessageInvite {
			if err := host.prepare(bot, player); err != nil {
				fmt.Printf("[YW2-VHOST] cannot prepare: %v\n", err)
				return message, nil, nil, nil
			}

			go func() {
				time.Sleep(eventInviteDelay)
				send(manager, message, binaryMessage, header, bot, player, eventMessageDecline, sendMessage)

				// * 2 人そろってから、それぞれに申し込む
				host.bridge.waitPrepared(2 * time.Minute)
				time.Sleep(vhostOfferWait)
				send(manager, message, binaryMessage, header, bot, player, eventMessageInvite, sendMessage)
				fmt.Printf("[YW2-VHOST] offered the room to %d\n", player)
			}()

			return message, nil, nil, nil
		}

		// * 開催していないイベントには、すぐ「断られた」を返す。一覧が古いままの人が誘ってきたとき
		if kind == eventMessageInvite && !globals.IsEventOpen(bot) {
			fmt.Printf("[YW2-EVENT] pid=%d invited closed event %d\n", player, bot)

			go func() {
				time.Sleep(eventInviteDelay)
				send(manager, message, binaryMessage, header, bot, player, eventMessageDecline, sendMessage)
			}()

			return message, nil, nil, nil
		}

		// * フリールール・レベル制限なし以外で誘った人は、すぐ断るだけにする（46_部屋主の交代の計画.md）。
		// * 断りの文は変えられない（いつも「相手は都合が悪いようです」）ので、待たせず、あとから申し込みも出さない。
		// * 正しいルールの 2 人目は断りのあと 2 秒ほどで申し込みが来るので、来なければ違うルールだと分かる
		if kind == eventMessageInvite && rule != eventRequiredRule && !globals.EventStyleEntry() {
			fmt.Printf("[YW2-EVENT] pid=%d chose rule %d in event %d, declined\n", player, rule, bot)

			go func() {
				time.Sleep(eventRuleDeclineDelay)
				send(manager, message, binaryMessage, header, bot, player, eventMessageDecline, sendMessage)
			}()

			return message, nil, nil, nil
		}

		if globals.EventStyleEntry() {
			// * エントリーの形: 記録して、すぐ「断られた」を返す。本人はランダム対戦を始める
			if kind == eventMessageInvite {
				globals.SetEventEntry(player, bot)
				fmt.Printf("[YW2-EVENT] pid=%d entered event %d rule=%d\n", player, bot, rule)

				go func() {
					time.Sleep(eventInviteDelay)
					send(manager, message, binaryMessage, header, bot, player, eventMessageDecline, sendMessage)
				}()
			}

			return message, nil, nil, nil
		}

		// * フィールド待機の形。キャンセルや、申し込みへの返事
		if kind != eventMessageInvite {
			if entrant, offered := globals.EventRoomOfferedTo(bot, player); offered {
				// * 部屋の持ち主がやめた。申し込みを受けていた人は待機に戻す
				send(manager, message, binaryMessage, header, bot, entrant, eventMessageCancel, sendMessage)
			}
			globals.ClearEventRoom(bot, player)
			fmt.Printf("[YW2-EVENT] pid=%d stopped in event %d, kind=%d\n", player, bot, kind)

			if kind == eventMessageDecline {
				// * 申し込みを断った人。待機から外して、部屋の持ち主に次の人を探す
				globals.ClearEventEntry(player)
				if host, offered := globals.EventRoomOfferedBy(bot, player); offered {
					globals.ReleaseEventClaim(bot, player)
					go offerEventRoom(bot, host)
				}
			}

			return message, nil, nil, nil
		}

		if history != nil {
			history.setMode(player, fmt.Sprintf("event:%d:%d", bot, rule))
		}

		// * このメッセージを、次の人を探すときにも使う
		next := func(target uint32, messageKind byte) {
			send(manager, message, binaryMessage, header, bot, target, messageKind, sendMessage)
		}
		eventOffersMutex.Lock()
		eventOffers[player] = next
		eventOffersMutex.Unlock()

		// * 部屋で待っている人がいれば、誘った人をその部屋へ申し込む
		if host, waiting := globals.WaitingEventRoom(bot, player); waiting {
			globals.OfferEventRoom(bot, host, player)
			fmt.Printf("[YW2-EVENT] pid=%d joins the waiting room of %d in event %d\n", player, host, bot)
			go offerWaitingRoom(bot, host, player, next)
			return message, nil, nil, nil
		}

		globals.SetEventRoom(bot, player)
		fmt.Printf("[YW2-EVENT] pid=%d invited event %d rule=%d\n", player, bot, rule)

		go offerEventRoom(bot, player)

		return message, nil, nil, nil
	}
}

var (
	eventOffersMutex sync.Mutex
	eventOffers      = make(map[uint32]func(target uint32, kind byte))
)

// * 管理画面の設定を読んで、一覧と受付に反映する。設定がないイベントは開催中として扱う。
// * 読めないときは今の状態を保つ
func syncEventSettings() {
	database := globals.MessagingManager.Database

	_, err := database.Exec(`
		CREATE SCHEMA IF NOT EXISTS history;
		CREATE TABLE IF NOT EXISTS history.event_settings (
			bot bigint PRIMARY KEY,
			open boolean NOT NULL,
			at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE IF NOT EXISTS history.event_rules (
			id bigserial PRIMARY KEY,
			name text NOT NULL,
			rules jsonb NOT NULL DEFAULT '{}',
			at timestamptz NOT NULL DEFAULT now()
		);
		ALTER TABLE history.event_settings ADD COLUMN IF NOT EXISTS rule_id bigint REFERENCES history.event_rules(id)`)
	if err != nil {
		common_globals.Logger.Error(err.Error())
		return
	}

	settings := make(map[uint32]bool)
	rows, err := database.Query(`SELECT s.bot, s.open, coalesce(r.rules, '{}') FROM history.event_settings s LEFT JOIN history.event_rules r ON r.id = s.rule_id`)
	if err != nil {
		common_globals.Logger.Error(err.Error())
		return
	}
	defer rows.Close()

	for rows.Next() {
		var bot int64
		var open bool
		var text []byte
		if err := rows.Scan(&bot, &open, &text); err != nil {
			common_globals.Logger.Error(err.Error())
			return
		}
		settings[uint32(bot)] = open

		// * 読めないルールなら、前のルールのままにする
		var rules globals.EventRules
		if err := json.Unmarshal(text, &rules); err != nil {
			common_globals.Logger.Error(err.Error())
			continue
		}
		globals.SetEventRules(uint32(bot), rules)
	}
	if err := rows.Err(); err != nil {
		common_globals.Logger.Error(err.Error())
		return
	}

	for _, bot := range globals.EventBots() {
		open, found := settings[bot]
		if !found {
			open = true
		}

		was := globals.IsEventOpen(bot)
		closing := globals.SetEventOpen(bot, open)
		if was != open {
			fmt.Printf("[YW2-EVENT] event %d open=%v\n", bot, open)
		}

		for _, room := range closing {
			eventOffersMutex.Lock()
			send := eventOffers[room.Host]
			delete(eventOffers, room.Host)
			eventOffersMutex.Unlock()

			if send == nil {
				continue
			}
			if room.OfferedTo != 0 {
				send(room.OfferedTo, eventMessageCancel)
			}
			send(room.Host, eventMessageDecline)
		}

		_, err := database.Exec(`UPDATE matchmaking.notifications SET active=$2 WHERE type=108000 AND source_pid=$1 AND active<>$2`, bot, open)
		if err != nil {
			common_globals.Logger.Error(err.Error())
		}
	}
}

// * 誘った人の部屋を、フィールドで待っている人の中で一番古い人に申し込む（ルールが違って待っている人など）。
// * 待っている人がいなければ、誘った人はそのまま自分の部屋で待つ
func offerEventRoom(bot uint32, host uint32) {
	eventOffersMutex.Lock()
	send := eventOffers[host]
	eventOffersMutex.Unlock()

	// * その間にやめた人は、待つ側に回さない
	if send == nil || !globals.IsEventRoomHost(host) {
		return
	}

	entrant, found := globals.NextEventEntrant(bot, host)
	if !found {
		fmt.Printf("[YW2-EVENT] pid=%d waits in the own room in event %d\n", host, bot)
		go releaseHeldRoom(bot, host, holdGeneration(host))
		return
	}

	globals.OfferEventRoom(bot, host, entrant)
	send(entrant, eventMessageInvite)
	fmt.Printf("[YW2-EVENT] offered room of %d to %d in event %d\n", host, entrant, bot)

	// * 返事がなければ、その人を待機から外して次の人へ
	time.Sleep(eventOfferLimit)
	if offeredTo, offered := globals.EventRoomOfferedTo(bot, host); offered && offeredTo == entrant {
		globals.ClearEventEntry(entrant)
		globals.ReleaseEventClaim(bot, entrant)
		send(entrant, eventMessageCancel)
		fmt.Printf("[YW2-EVENT] %d did not answer in event %d\n", entrant, bot)
		offerEventRoom(bot, host)
	}
}

var (
	eventMessageMutex sync.Mutex
	eventInvitedAt    = make(map[uint32]time.Time)
)

// * 記録: 誘いから何秒で、どの種類の知らせが来たか
func noteEventMessage(player uint32, bot uint32, kind byte, body []byte) {
	eventMessageMutex.Lock()
	defer eventMessageMutex.Unlock()

	now := time.Now()
	if kind == eventMessageInvite {
		eventInvitedAt[player] = now
		fmt.Printf("[YW2-EVENT] pid=%d message to %d kind=%d body=%x\n", player, bot, kind, body)
		return
	}
	if at, found := eventInvitedAt[player]; found {
		fmt.Printf("[YW2-EVENT] pid=%d message to %d kind=%d body=%x after %.1fs\n", player, bot, kind, body, now.Sub(at).Seconds())
		delete(eventInvitedAt, player)
		return
	}
	fmt.Printf("[YW2-EVENT] pid=%d message to %d kind=%d body=%x\n", player, bot, kind, body)
}

// * 待っている部屋への申し込みを、誘ってきた 2 人目に出す。
// * 2 人目は誘った直後で返事待ちの画面なので、先に断ってフィールドに戻す。そうしないと申し込みを拾えない
func offerWaitingRoom(bot uint32, host uint32, entrant uint32, send func(target uint32, kind byte)) {
	time.Sleep(eventInviteDelay)
	send(entrant, eventMessageDecline)
	time.Sleep(eventInviteDelay)
	send(entrant, eventMessageInvite)
	fmt.Printf("[YW2-EVENT] offered waiting room of %d to %d in event %d\n", host, entrant, bot)

	// * 返事がなければ、部屋をまた空ける（1 人目は待ち続ける）
	time.Sleep(eventOfferLimit)
	if offeredTo, offered := globals.EventRoomOfferedTo(bot, host); offered && offeredTo == entrant {
		globals.ReleaseEventClaim(bot, entrant)
		send(entrant, eventMessageCancel)
		fmt.Printf("[YW2-EVENT] %d did not answer the waiting room in event %d\n", entrant, bot)
	}
}

var (
	eventHoldMutex       sync.Mutex
	eventHoldGenerations = make(map[uint32]uint64)
)

// * 部屋で待ち始めるたびに番号を進める。前の待機の帰す処理が、新しい待機を帰さないように
func holdGeneration(host uint32) uint64 {
	eventHoldMutex.Lock()
	defer eventHoldMutex.Unlock()

	eventHoldGenerations[host]++
	return eventHoldGenerations[host]
}

// * 長く待っても相手が来なければ、断ってフィールドに戻す（部屋の記録が消えて、誰も入れなくなる前に）
func releaseHeldRoom(bot uint32, host uint32, generation uint64) {
	time.Sleep(eventHoldLimit)

	eventHoldMutex.Lock()
	current := eventHoldGenerations[host] == generation
	eventHoldMutex.Unlock()
	if !current || !globals.IsEventRoomHost(host) {
		return
	}
	if _, offered := globals.EventRoomOfferedTo(bot, host); offered {
		return
	}

	eventOffersMutex.Lock()
	send := eventOffers[host]
	eventOffersMutex.Unlock()
	if send == nil {
		return
	}

	globals.ClearEventRoom(bot, host)
	send(host, eventMessageDecline)
	fmt.Printf("[YW2-EVENT] pid=%d waited too long in event %d, sent back to the field\n", host, bot)
}
