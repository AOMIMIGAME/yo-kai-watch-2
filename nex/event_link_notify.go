package nex

import (
	"encoding/base64"
	"fmt"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	common_globals "github.com/PretendoNetwork/nex-protocols-common-go/v2/globals"
	"github.com/PretendoNetwork/nex-protocols-common-go/v2/matchmake-extension/database"
	matchmakeextension "github.com/PretendoNetwork/nex-protocols-go/v2/matchmake-extension"
	notifications_constants "github.com/PretendoNetwork/nex-protocols-go/v2/notifications/constants"
	notifications_types "github.com/PretendoNetwork/nex-protocols-go/v2/notifications/types"

	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

// * 紐付けの bot の名前を、見に来た人ごとの英数字にする（ほかの人には、その人の英数字が見える）。
// * ゲームは誘いの画面でも、ここで取った名前を使う。共通の処理と同じ形で返し、名前だけを差し替える
// * bot 以外の名前は、なりすましに使える文字列を ■ に置き換えて送る（name_filter.go）
func registerLinkNotifications(protocol *matchmakeextension.Protocol) {

	respond := func(packet nex.PacketInterface, callID uint32, methodID uint32, types []uint32) (*nex.RMCMessage, *nex.Error) {
		connection := packet.Sender().(*nex.PRUDPConnection)
		endpoint := connection.Endpoint().(*nex.PRUDPEndPoint)

		manager := globals.MatchmakingManager
		manager.Mutex.RLock()
		datas, nexError := database.GetNotificationDatas(manager, connection.PID(), types)
		manager.Mutex.RUnlock()
		if nexError != nil {
			return nil, nexError
		}

		pid := uint32(connection.PID())
		for n := range datas {
			source := uint32(datas[n].PIDSource)
			if linkTestBot != 0 && source == linkTestBot {
				datas[n].StrParam = typesString(linkNotificationName(linkCodeFor(pid)))
				continue
			}
			if globals.IsEventBot(source) {
				continue
			}
			if filtered, changed := filterNotificationName(string(datas[n].StrParam)); changed {
				datas[n].StrParam = typesString(filtered)
				fmt.Printf("[YW2-NAME] name of pid=%d filtered for pid=%d\n", source, pid)
			}
		}

		stream := nex.NewByteStreamOut(endpoint.LibraryVersions(), endpoint.ByteStreamSettings())
		list := typesNotificationList(datas)
		list.WriteTo(stream)

		response := nex.NewRMCSuccess(endpoint, stream.Bytes())
		response.ProtocolID = matchmakeextension.ProtocolID
		response.MethodID = methodID
		response.CallID = callID
		return response, nil
	}

	protocol.SetHandlerGetFriendNotificationData(func(err error, packet nex.PacketInterface, callID uint32, uiType notifications_constants.NotificationCategorySigned) (*nex.RMCMessage, *nex.Error) {
		if err != nil {
			common_globals.Logger.Error(err.Error())
			return nil, nex.NewError(nex.ResultCodes.Core.InvalidArgument, err.Error())
		}
		if uiType < 101 || uiType > 108 {
			return nil, nex.NewError(nex.ResultCodes.Core.InvalidArgument, "change_error")
		}
		return respond(packet, callID, matchmakeextension.MethodGetFriendNotificationData, []uint32{uint32(uiType.ToUnsigned().Build())})
	})

	protocol.SetHandlerGetlstFriendNotificationData(func(err error, packet nex.PacketInterface, callID uint32, lstTypes types.List[notifications_constants.NotificationCategory]) (*nex.RMCMessage, *nex.Error) {
		if err != nil {
			common_globals.Logger.Error(err.Error())
			return nil, nex.NewError(nex.ResultCodes.Core.InvalidArgument, err.Error())
		}
		list := make([]uint32, len(lstTypes))
		for n, t := range lstTypes {
			if t < 101 || t > 108 {
				return nil, nex.NewError(nex.ResultCodes.Core.InvalidArgument, "change_error")
			}
			list[n] = uint32(t.Build())
		}
		return respond(packet, callID, matchmakeextension.MethodGetlstFriendNotificationData, list)
	})
}

// * 「認証:」（Shift-JIS）
var linkNamePrefix = []byte{0x94, 0x46, 0x8F, 0xD8, ':'}

// * 名前の欄（28 バイト、Shift-JIS、0 で終わる）を base64 にしたもの。「認証:」+ 英数字（英数字はそのまま Shift-JIS）
func linkNotificationName(code string) string {
	field := make([]byte, 28)
	n := copy(field, linkNamePrefix)
	copy(field[n:27], code)
	return base64.StdEncoding.EncodeToString(field)
}

func typesString(value string) types.String {
	return types.String(value)
}

func typesNotificationList(datas []notifications_types.NotificationEvent) types.List[notifications_types.NotificationEvent] {
	return types.List[notifications_types.NotificationEvent](datas)
}
