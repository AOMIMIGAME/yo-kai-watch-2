package globals

import (
	"os"
	"sync"
	"time"
)

// * イベント用の部屋。偽のプレイヤーを誘った人が持ち主になり、誘った順に並ぶ
const (
	eventRoomLimit  = 30 * time.Minute
	eventClaimLimit = 90 * time.Second // * 申し込みを受けた人が入るまでの猶予。過ぎたら次の人へ
	eventEntryLimit = 30 * time.Minute // * エントリーの有効時間
)

type eventEntry struct {
	bot uint32
	at  time.Time
}

type eventRoom struct {
	host      uint32
	at        time.Time
	claimedBy uint32
	claimedAt time.Time
	accepted  bool // * 申し込まれた人が部屋を探しに来た。対戦に入るところ
}

// EventRoomToClose is a waiting room removed because the event was closed
type EventRoomToClose struct {
	Host      uint32
	OfferedTo uint32
}

var (
	eventRoomsMutex sync.Mutex
	eventRooms      = make(map[uint32][]*eventRoom)
	eventEntries    = make(map[uint32]eventEntry)
	eventClosed     = make(map[uint32]bool)

	// EventRoomAlive reports whether a host is still connected. Set by the secure server
	EventRoomAlive func(pid uint32) bool
)

// IsEventBot returns whether the PID is a fake player used for events
func IsEventBot(pid uint32) bool {
	for _, bot := range eventBotPIDs {
		if bot == pid {
			return true
		}
	}

	return false
}

// EventBots returns the PIDs of the fake players
func EventBots() []uint32 {
	return eventBotPIDs
}

func removeEventRoom(rooms []*eventRoom, host uint32) []*eventRoom {
	kept := rooms[:0]
	for _, room := range rooms {
		if room.host != host {
			kept = append(kept, room)
		}
	}

	return kept
}

// SetEventRoom adds the player to the end of the queue. Inviting again moves them to the end
func SetEventRoom(bot uint32, host uint32) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	eventRooms[bot] = append(removeEventRoom(eventRooms[bot], host), &eventRoom{host: host, at: time.Now()})
}

// ClearEventRoom forgets the player's room for the given fake player
func ClearEventRoom(bot uint32, host uint32) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	eventRooms[bot] = removeEventRoom(eventRooms[bot], host)
}

// ClearEventRoomsOf forgets all rooms of the player, when their room closes
func ClearEventRoomsOf(host uint32) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	for bot := range eventRooms {
		eventRooms[bot] = removeEventRoom(eventRooms[bot], host)
	}
}

// ClaimEventRoom returns the oldest waiting room for the player looking for the fake player
func ClaimEventRoom(bot uint32, seeker uint32) (uint32, bool) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	now := time.Now()
	kept := eventRooms[bot][:0]
	found := uint32(0)

	for _, room := range eventRooms[bot] {
		if now.Sub(room.at) > eventRoomLimit || (EventRoomAlive != nil && !EventRoomAlive(room.host)) {
			continue
		}

		kept = append(kept, room)

		if found != 0 || room.host == seeker {
			continue
		}

		if room.claimedBy == 0 || room.claimedBy == seeker || now.Sub(room.claimedAt) > eventClaimLimit {
			room.claimedBy = seeker
			room.claimedAt = now
			room.accepted = true
			found = room.host
		}
	}

	eventRooms[bot] = kept

	return found, found != 0
}

// ReleaseEventClaim lets the next player take the room the given player was offered
func ReleaseEventClaim(bot uint32, seeker uint32) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	for _, room := range eventRooms[bot] {
		if room.claimedBy == seeker {
			room.claimedBy = 0
			room.accepted = false
		}
	}
}

// IsEventOpen reports whether the event takes players. Events are open until closed
func IsEventOpen(bot uint32) bool {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	return !eventClosed[bot]
}

// SetEventOpen opens or closes the event. Closing forgets everyone waiting and returns the rooms
// whose players must be told. Rooms already going into a battle are not returned, so the battle goes on
func SetEventOpen(bot uint32, open bool) []EventRoomToClose {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	if open {
		delete(eventClosed, bot)
		return nil
	}

	if eventClosed[bot] {
		return nil
	}
	eventClosed[bot] = true

	var closing []EventRoomToClose
	for _, room := range eventRooms[bot] {
		if room.accepted || (EventRoomAlive != nil && !EventRoomAlive(room.host)) {
			continue
		}

		closing = append(closing, EventRoomToClose{Host: room.host, OfferedTo: room.claimedBy})
	}
	delete(eventRooms, bot)

	for pid, entry := range eventEntries {
		if entry.bot == bot {
			delete(eventEntries, pid)
		}
	}

	return closing
}

// EventStyleEntry reports the entry style: inviting a fake player only registers the player for the
// event, and they then use the normal random match. The other style keeps friend battle rooms
func EventStyleEntry() bool {
	return os.Getenv("YW2_EVENT_STYLE") == "entry"
}

// SetEventEntry registers the player for the event until the entry expires
func SetEventEntry(pid uint32, bot uint32) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	eventEntries[pid] = eventEntry{bot: bot, at: time.Now()}
}

// ClearEventEntry removes the player's entry
func ClearEventEntry(pid uint32) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	delete(eventEntries, pid)
}

// EventEntry returns the fake player of the event the player entered
func EventEntry(pid uint32) (uint32, bool) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	entry, ok := eventEntries[pid]
	if !ok {
		return 0, false
	}

	if time.Since(entry.at) > eventEntryLimit {
		delete(eventEntries, pid)
		return 0, false
	}

	return entry.bot, true
}

// EventSlot returns the search value of the player: 0 for the normal random match, 1 or more for an event
func EventSlot(pid uint32) uint32 {
	bot, ok := EventEntry(pid)
	if !ok {
		return 0
	}

	for i, candidate := range eventBotPIDs {
		if candidate == bot {
			return uint32(i) + 1
		}
	}

	return 0
}

// NextEventEntrant returns the player waiting longest for the event, other than the given player
func NextEventEntrant(bot uint32, exclude uint32) (uint32, bool) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	now := time.Now()
	found := uint32(0)
	var oldest time.Time

	for pid, entry := range eventEntries {
		if entry.bot != bot || pid == exclude {
			continue
		}

		if now.Sub(entry.at) > eventEntryLimit || (EventRoomAlive != nil && !EventRoomAlive(pid)) {
			delete(eventEntries, pid)
			continue
		}

		// * すでに別の部屋の申し込みを受けている人は飛ばす
		busy := false
		for _, room := range eventRooms[bot] {
			if room.claimedBy == pid {
				busy = true
				break
			}
		}

		if !busy && (found == 0 || entry.at.Before(oldest)) {
			found = pid
			oldest = entry.at
		}
	}

	return found, found != 0
}

// OfferEventRoom reserves the host's room for the waiting player
func OfferEventRoom(bot uint32, host uint32, entrant uint32) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	for _, room := range eventRooms[bot] {
		if room.host == host {
			room.claimedBy = entrant
			room.claimedAt = time.Now()
		}
	}
}

// EventRoomOfferedTo returns who the host's room is reserved for
func EventRoomOfferedTo(bot uint32, host uint32) (uint32, bool) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	for _, room := range eventRooms[bot] {
		if room.host == host && room.claimedBy != 0 {
			return room.claimedBy, true
		}
	}

	return 0, false
}

// EventRoomOfferedBy returns whose room is reserved for the player
func EventRoomOfferedBy(bot uint32, entrant uint32) (uint32, bool) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	for _, room := range eventRooms[bot] {
		if room.claimedBy == entrant {
			return room.host, true
		}
	}

	return 0, false
}

// IsEventRoomHost returns whether the player owns an event room
func IsEventRoomHost(pid uint32) bool {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	for _, rooms := range eventRooms {
		for _, room := range rooms {
			if room.host == pid && time.Since(room.at) <= eventRoomLimit {
				return true
			}
		}
	}

	return false
}

// WaitingEventRoom returns the oldest room of the event that nobody has been offered yet (the host waits in it)
func WaitingEventRoom(bot uint32, seeker uint32) (uint32, bool) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	now := time.Now()
	for _, room := range eventRooms[bot] {
		if room.host == seeker || now.Sub(room.at) > eventRoomLimit || (EventRoomAlive != nil && !EventRoomAlive(room.host)) {
			continue
		}
		if room.claimedBy == 0 || now.Sub(room.claimedAt) > eventClaimLimit {
			return room.host, true
		}
	}

	return 0, false
}
