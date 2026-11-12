package globals

import (
	"testing"
	"time"
)

func resetEventRooms() {
	eventRooms = make(map[uint32][]*eventRoom)
	eventEntries = make(map[uint32]eventEntry)
	eventClosed = make(map[uint32]bool)
	EventRoomAlive = nil
}

func TestEventRoomsKeepOrder(t *testing.T) {
	resetEventRooms()
	SetEventRoom(1, 10)
	SetEventRoom(1, 20)

	if host, ok := ClaimEventRoom(1, 30); !ok || host != 10 {
		t.Fatalf("first: %d %v", host, ok)
	}

	// * 同じ人が探し直しても同じ部屋。別の人は次の部屋
	if host, ok := ClaimEventRoom(1, 30); !ok || host != 10 {
		t.Fatalf("retry: %d %v", host, ok)
	}
	if host, ok := ClaimEventRoom(1, 40); !ok || host != 20 {
		t.Fatalf("next: %d %v", host, ok)
	}
	if _, ok := ClaimEventRoom(1, 50); ok {
		t.Fatal("all rooms are claimed")
	}
}

func TestCloseEvent(t *testing.T) {
	resetEventRooms()

	if !IsEventOpen(1) {
		t.Fatal("events start open")
	}

	SetEventRoom(1, 10) // * 相手待ち
	SetEventRoom(1, 20) // * 申し込み中
	OfferEventRoom(1, 20, 21)
	SetEventRoom(1, 30) // * 対戦に入るところ
	if host, ok := ClaimEventRoom(1, 31); !ok || host != 10 {
		t.Fatalf("setup claim: %d %v", host, ok)
	}
	SetEventRoom(2, 40) // * 別のイベント
	SetEventEntry(50, 1)
	SetEventEntry(60, 2)

	closing := SetEventOpen(1, false)
	if len(closing) != 2 || closing[0] != (EventRoomToClose{Host: 20, OfferedTo: 21}) || closing[1] != (EventRoomToClose{Host: 30}) {
		t.Fatalf("closing: %+v", closing)
	}
	if IsEventOpen(1) || !IsEventOpen(2) {
		t.Fatal("wrong event closed")
	}
	if _, ok := EventEntry(50); ok {
		t.Fatal("entry stayed")
	}
	if _, ok := EventEntry(60); !ok {
		t.Fatal("entry of another event removed")
	}
	if _, ok := ClaimEventRoom(1, 70); ok {
		t.Fatal("room stayed")
	}
	if host, ok := ClaimEventRoom(2, 70); !ok || host != 40 {
		t.Fatalf("other event: %d %v", host, ok)
	}

	// * 閉じたままなら、もう一度は知らせない
	if closing := SetEventOpen(1, false); closing != nil {
		t.Fatalf("closed twice: %+v", closing)
	}

	SetEventOpen(1, true)
	if !IsEventOpen(1) {
		t.Fatal("not reopened")
	}
}

func TestEventRoomsAreSeparatePerEvent(t *testing.T) {
	resetEventRooms()
	SetEventRoom(1, 10)

	if _, ok := ClaimEventRoom(2, 30); ok {
		t.Fatal("found a room of another event")
	}
}

func TestEventRoomNotOwnRoom(t *testing.T) {
	resetEventRooms()
	SetEventRoom(1, 10)

	if _, ok := ClaimEventRoom(1, 10); ok {
		t.Fatal("found own room")
	}
}

func TestEventRoomCancelAndClose(t *testing.T) {
	resetEventRooms()
	SetEventRoom(1, 10)
	SetEventRoom(1, 20)
	ClearEventRoom(1, 10)

	if host, ok := ClaimEventRoom(1, 30); !ok || host != 20 {
		t.Fatalf("after cancel: %d %v", host, ok)
	}
	if !IsEventRoomHost(20) || IsEventRoomHost(10) {
		t.Fatal("host check")
	}

	ClearEventRoomsOf(20)
	if IsEventRoomHost(20) {
		t.Fatal("closed room kept")
	}
}

func TestEventClaimReleasedOnDecline(t *testing.T) {
	resetEventRooms()
	SetEventRoom(1, 10)

	if host, ok := ClaimEventRoom(1, 30); !ok || host != 10 {
		t.Fatalf("first: %d %v", host, ok)
	}
	if _, ok := ClaimEventRoom(1, 40); ok {
		t.Fatal("room offered twice")
	}

	// * 断ったら、部屋の持ち主はそのまま次の人を待てる
	ReleaseEventClaim(1, 30)
	if host, ok := ClaimEventRoom(1, 40); !ok || host != 10 {
		t.Fatalf("after decline: %d %v", host, ok)
	}
}

func TestEventEntrySlots(t *testing.T) {
	eventEntries = make(map[uint32]eventEntry)
	eventBotPIDs = []uint32{100, 200}

	if slot := EventSlot(1); slot != 0 {
		t.Fatalf("no entry: %d", slot)
	}

	SetEventEntry(1, 100)
	SetEventEntry(2, 200)
	if EventSlot(1) != 1 || EventSlot(2) != 2 {
		t.Fatalf("slots: %d %d", EventSlot(1), EventSlot(2))
	}

	ClearEventEntry(1)
	if EventSlot(1) != 0 {
		t.Fatal("cleared entry kept")
	}

	eventEntries[2] = eventEntry{bot: 200, at: time.Now().Add(-eventEntryLimit - time.Second)}
	if EventSlot(2) != 0 {
		t.Fatal("expired entry kept")
	}
}

func TestNextEventEntrantOrder(t *testing.T) {
	resetEventRooms()
	eventEntries = make(map[uint32]eventEntry)
	now := time.Now()
	eventEntries[1] = eventEntry{bot: 100, at: now.Add(-time.Minute)}
	eventEntries[2] = eventEntry{bot: 100, at: now.Add(-2 * time.Minute)}
	eventEntries[3] = eventEntry{bot: 200, at: now.Add(-3 * time.Minute)}

	// * 同じイベントで一番古い人。本人と別のイベントは除く
	if pid, ok := NextEventEntrant(100, 9); !ok || pid != 2 {
		t.Fatalf("oldest: %d %v", pid, ok)
	}
	if pid, ok := NextEventEntrant(100, 2); !ok || pid != 1 {
		t.Fatalf("exclude: %d %v", pid, ok)
	}

	// * 申し込み中の人は飛ばす
	SetEventRoom(100, 9)
	OfferEventRoom(100, 9, 2)
	if pid, ok := NextEventEntrant(100, 8); !ok || pid != 1 {
		t.Fatalf("busy: %d %v", pid, ok)
	}
	if entrant, ok := EventRoomOfferedTo(100, 9); !ok || entrant != 2 {
		t.Fatalf("offered to: %d %v", entrant, ok)
	}
	if host, ok := EventRoomOfferedBy(100, 2); !ok || host != 9 {
		t.Fatalf("offered by: %d %v", host, ok)
	}

	// * 申し込まれた本人だけが、その部屋を受け取れる
	if _, ok := ClaimEventRoom(100, 8); ok {
		t.Fatal("reserved room taken")
	}
	if host, ok := ClaimEventRoom(100, 2); !ok || host != 9 {
		t.Fatalf("claim: %d %v", host, ok)
	}

	// * 古い順は時間の切れた人を消す
	eventEntries[1] = eventEntry{bot: 100, at: now.Add(-eventEntryLimit - time.Minute)}
	if _, ok := eventEntries[1]; !ok {
		t.Fatal("setup")
	}
	ReleaseEventClaim(100, 2)
	ClearEventEntry(2)
	if pid, ok := NextEventEntrant(100, 8); ok {
		t.Fatalf("expired returned: %d", pid)
	}
}

func TestEventRoomSkipsDisconnectedAndStale(t *testing.T) {
	resetEventRooms()
	SetEventRoom(1, 10)
	SetEventRoom(1, 20)
	EventRoomAlive = func(pid uint32) bool { return pid != 10 }

	if host, ok := ClaimEventRoom(1, 30); !ok || host != 20 {
		t.Fatalf("disconnected: %d %v", host, ok)
	}

	// * 探した人が入らなかった部屋は、猶予の後に次の人へ
	eventRooms[1][0].claimedAt = time.Now().Add(-eventClaimLimit - time.Second)
	if host, ok := ClaimEventRoom(1, 40); !ok || host != 20 {
		t.Fatalf("stale claim: %d %v", host, ok)
	}

	eventRooms[1][0].at = time.Now().Add(-eventRoomLimit - time.Second)
	if _, ok := ClaimEventRoom(1, 50); ok {
		t.Fatal("expired room found")
	}
}

// * 待っている部屋: 申し込み中の部屋と自分の部屋は飛ばす
func TestWaitingEventRoom(t *testing.T) {
	resetEventRooms()
	SetEventRoom(9, 1)
	SetEventRoom(9, 2)
	if host, ok := WaitingEventRoom(9, 3); !ok || host != 1 {
		t.Fatalf("waiting room %d %v", host, ok)
	}
	OfferEventRoom(9, 1, 3)
	if host, ok := WaitingEventRoom(9, 4); !ok || host != 2 {
		t.Fatalf("after offer %d %v", host, ok)
	}
	if host, ok := WaitingEventRoom(9, 2); ok {
		t.Fatalf("own room %d", host)
	}
}
