package nex_match_making

import (
	"fmt"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	common_globals "github.com/PretendoNetwork/nex-protocols-common-go/v2/globals"
	match_making "github.com/PretendoNetwork/nex-protocols-go/v2/match-making"
	match_making_types "github.com/PretendoNetwork/nex-protocols-go/v2/match-making/types"
	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
	nex_match_making_database "github.com/PretendoNetwork/yo-kai-watch-2/nex/match-making/database"
)

func FindByOwner(err error, packet nex.PacketInterface, callID uint32, id types.PID, resultRange types.ResultRange) (*nex.RMCMessage, *nex.Error) {
	if err != nil {
		common_globals.Logger.Error(err.Error())
		return nil, nex.NewError(nex.ResultCodes.Core.InvalidArgument, "change_error")
	}

	connection := packet.Sender().(*nex.PRUDPConnection)
	endpoint := connection.Endpoint().(*nex.PRUDPEndPoint)

	// * イベント用の偽のプレイヤーを探されたら、待っている人の部屋を返す
	// * 試験用: サーバーが持ち主を演じる部屋。持ち主は偽のプレイヤーのままで、部屋は 1 人に 1 つ
	virtualRoom, isVirtual := globals.EventVirtualHostRoom(uint32(connection.PID()), uint32(id))
	if isVirtual {
		fmt.Printf("[YW2-VHOST] pid=%d looked for room %d\n", uint32(connection.PID()), virtualRoom)
	} else if globals.IsEventBot(uint32(id)) {
		host, ok := globals.ClaimEventRoom(uint32(id), uint32(connection.PID()))
		fmt.Printf("[YW2-EVENT] pid=%d looked for event %d: host=%d found=%v\n", uint32(connection.PID()), uint32(id), host, ok)
		if ok {
			id = types.PID(host)
			globals.ClearEventEntry(uint32(connection.PID()))
		}
	}

	globals.MatchmakingManager.Mutex.RLock()

	var gatheringIDs []uint32
	var nexError *nex.Error
	if isVirtual {
		gatheringIDs = []uint32{virtualRoom}
	} else {
		gatheringIDs, nexError = nex_match_making_database.FindGatheringIDsByOwner(globals.MatchmakingManager, connection, id, resultRange)
		if nexError != nil {
			common_globals.Logger.Error(nexError.Error())
			globals.MatchmakingManager.Mutex.RUnlock()
			return nil, nexError
		}
	}

	gatheringHolders := types.NewList[match_making_types.GatheringHolder]()
	if len(gatheringIDs) > 0 {
		gatheringHolders, nexError = nex_match_making_database.GetDetailedGatheringsByID(globals.MatchmakingManager, uint64(connection.PID()), gatheringIDs)
		if nexError != nil {
			common_globals.Logger.Error(nexError.Error())
			globals.MatchmakingManager.Mutex.RUnlock()
			return nil, nexError
		}
	}

	globals.MatchmakingManager.Mutex.RUnlock()

	rmcResponseStream := nex.NewByteStreamOut(endpoint.LibraryVersions(), endpoint.ByteStreamSettings())

	gatheringHolders.WriteTo(rmcResponseStream)

	rmcResponseBody := rmcResponseStream.Bytes()

	rmcResponse := nex.NewRMCSuccess(endpoint, rmcResponseBody)
	rmcResponse.ProtocolID = match_making.ProtocolID
	rmcResponse.MethodID = match_making.MethodFindByOwner
	rmcResponse.CallID = callID

	return rmcResponse, nil
}
