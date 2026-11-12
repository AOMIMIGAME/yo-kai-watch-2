package nex

import (
	"fmt"
	"os"
	"strconv"

	"github.com/PretendoNetwork/nex-go/v2"
	common_globals "github.com/PretendoNetwork/nex-protocols-common-go/v2/globals"
	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

func StartSecureServer() {
	globals.SecureServer = nex.NewPRUDPServer()
	globals.SecureServer.ByteStreamSettings.UseStructureHeader = true

	globals.SecureEndpoint = nex.NewPRUDPEndPoint(1)
	globals.SecureEndpoint.IsSecureEndPoint = true
	globals.SecureEndpoint.ServerAccount = globals.SecureServerAccount
	globals.SecureEndpoint.AccountDetailsByPID = globals.AccountDetailsByPID
	globals.SecureEndpoint.AccountDetailsByUsername = globals.AccountDetailsByUsername
	globals.SecureServer.BindPRUDPEndPoint(globals.SecureEndpoint)

	globals.SecureServer.LibraryVersions.SetDefault(nex.NewLibraryVersion(3, 6, 1))
	globals.SecureServer.AccessKey = "7ab183bb"

	globals.SecureEndpoint.OnData(func(packet nex.PacketInterface) {
		request := packet.RMCMessage()

		//trace is never getting what they want
		fmt.Println("==Yo-kai Watch 2- Secure==")
		fmt.Printf("Protocol ID: %d\n", request.ProtocolID)
		fmt.Printf("Method ID: %d\n", request.MethodID)
		fmt.Println("===============")

		if history != nil {
			safely("history call", func() { history.onCall(packet) })
		}

		// * 部屋を閉じた、または解散した人は、イベントの待ち行列から外す
		if (request.ProtocolID == 109 && request.MethodID == 1) || (request.ProtocolID == 21 && request.MethodID == 2) {
			globals.ClearEventRoomsOf(uint32(packet.Sender().PID()))
		}

		// * 試験用の記録: 対戦記録の検討のため、対戦の前後に届くデータをそのまま出す
		if os.Getenv("YW2_DEV_DUMP_CALLS") == "true" {
			dump := os.Getenv("YW2_DEV_DUMP_ALL") == "true" || request.ProtocolID == 110 ||
				(request.ProtocolID == 112 && (request.MethodID == 1 || request.MethodID == 4)) ||
				(request.ProtocolID == 109 && request.MethodID == 15)
			if dump {
				fmt.Printf("[YW2-DUMP] pid=%d protocol=%d method=%d size=%d hex=%x\n", uint32(packet.Sender().PID()), request.ProtocolID, request.MethodID, len(request.Parameters), request.Parameters)
			}
		}
	})

	globals.SecureEndpoint.OnError(func(err *nex.Error) {
		globals.Logger.Errorf("Secure: %v", err)
	})

	globals.MatchmakingManager = common_globals.NewMatchmakingManager(globals.SecureEndpoint, globals.Postgres)
	globals.MessagingManager = common_globals.NewMessagingManager(globals.SecureEndpoint, globals.Postgres)
	globals.UtilityManager = common_globals.NewUtilityManager(globals.SecureEndpoint, globals.Postgres)

	startBattleHistory()

	registerCommonSecureServerProtocols()

	port, _ := strconv.Atoi(os.Getenv("PN_YKW2_SECURE_SERVER_PORT"))

	globals.SecureServer.Listen(port)
}
