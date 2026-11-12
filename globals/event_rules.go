package globals

// EventRankLimit is how many yo-kai of a rank a party may or must have. Nil means no limit
type EventRankLimit struct {
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
}

// EventSpeciesLimit is how many of a yo-kai a party may or must have. Yo-kai are matched by name
type EventSpeciesLimit struct {
	Name string `json:"name"`
	Min  *int   `json:"min,omitempty"`
	Max  *int   `json:"max,omitempty"`
}

// EventRules is what the server checks in an event battle. Made in the admin page
type EventRules struct {
	Ranks   map[string]EventRankLimit `json:"ranks,omitempty"`
	Species []EventSpeciesLimit       `json:"species,omitempty"`
	Level   *int                      `json:"level,omitempty"` // * Only this level is allowed
}

var eventRules = make(map[uint32]EventRules)

// * 試験用: サーバーが持ち主を演じる部屋。部屋は 1 人に 1 つ
var eventVirtualHostRooms = make(map[uint32]eventVirtualHostRoom)

type eventVirtualHostRoom struct {
	bot uint32
	gid uint32
}

// SetEventVirtualHost marks the room hosted by the server for the player. Test only
func SetEventVirtualHost(player uint32, bot uint32, gid uint32) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	eventVirtualHostRooms[player] = eventVirtualHostRoom{bot: bot, gid: gid}
}

// EventVirtualHostRoom returns the room hosted by the server for the player looking for the fake player
func EventVirtualHostRoom(player uint32, bot uint32) (uint32, bool) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	room, found := eventVirtualHostRooms[player]
	if !found || room.bot != bot {
		return 0, false
	}
	return room.gid, true
}

// SetEventRules replaces the rules of the event
func SetEventRules(bot uint32, rules EventRules) {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	eventRules[bot] = rules
}

// EventRulesOf returns the rules of the event. No rules means nothing is checked
func EventRulesOf(bot uint32) EventRules {
	eventRoomsMutex.Lock()
	defer eventRoomsMutex.Unlock()

	return eventRules[bot]
}
