package nex

import (
	//"fmt"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	common_globals "github.com/PretendoNetwork/nex-protocols-common-go/v2/globals"
	commonnattraversal "github.com/PretendoNetwork/nex-protocols-common-go/v2/nat-traversal"
	commonsecure "github.com/PretendoNetwork/nex-protocols-common-go/v2/secure-connection"
	nattraversal "github.com/PretendoNetwork/nex-protocols-go/v2/nat-traversal"
	secure "github.com/PretendoNetwork/nex-protocols-go/v2/secure-connection"
	"github.com/PretendoNetwork/yo-kai-watch-2/globals"

	commonmatchmaking "github.com/PretendoNetwork/nex-protocols-common-go/v2/match-making"
	commonmatchmakingext "github.com/PretendoNetwork/nex-protocols-common-go/v2/match-making-ext"
	commonmatchmakeextension "github.com/PretendoNetwork/nex-protocols-common-go/v2/matchmake-extension"
	commonmessagedelivery "github.com/PretendoNetwork/nex-protocols-common-go/v2/message-delivery"
	commonutility "github.com/PretendoNetwork/nex-protocols-common-go/v2/utility"
	matchmaking "github.com/PretendoNetwork/nex-protocols-go/v2/match-making"
	matchmakingext "github.com/PretendoNetwork/nex-protocols-go/v2/match-making-ext"
	matchmakeextension "github.com/PretendoNetwork/nex-protocols-go/v2/matchmake-extension"

	matchmakingtypes "github.com/PretendoNetwork/nex-protocols-go/v2/match-making/types"
	messagedelivery "github.com/PretendoNetwork/nex-protocols-go/v2/message-delivery"
	ranking "github.com/PretendoNetwork/nex-protocols-go/v2/ranking"
	utility "github.com/PretendoNetwork/nex-protocols-go/v2/utility"

	local_match_making "github.com/PretendoNetwork/yo-kai-watch-2/nex/match-making"
	local_matchmake_extension "github.com/PretendoNetwork/yo-kai-watch-2/nex/matchmake-extension"
)

func CreateReportDBRecord(_ types.PID, _ types.UInt32, _ types.QBuffer) error {
	return nil
}

func registerCommonSecureServerProtocols() {
	secureProtocol := secure.NewProtocol()
	globals.SecureEndpoint.RegisterServiceProtocol(secureProtocol)
	commonSecureProtocol := commonsecure.NewCommonProtocol(secureProtocol)
	commonSecureProtocol.EnableInsecureRegister()

	globals.MatchmakingManager.GetUserFriendPIDs = globals.GetUserFriendPIDs

	commonSecureProtocol.CreateReportDBRecord = CreateReportDBRecord


	natTraversalProtocol := nattraversal.NewProtocol()
	globals.SecureEndpoint.RegisterServiceProtocol(natTraversalProtocol)
	commonnattraversal.NewCommonProtocol(natTraversalProtocol)

	// * 試験用の中継。YW2_RELAY=true のときだけ
	registerPacketRelay(secureProtocol, natTraversalProtocol)

	matchMakingProtocol := matchmaking.NewProtocol()
	globals.SecureEndpoint.RegisterServiceProtocol(matchMakingProtocol)
	matchMakingProtocol.FindByOwner = local_match_making.FindByOwner
	commonmatchmaking.NewCommonProtocol(matchMakingProtocol).SetManager(globals.MatchmakingManager)
	registerVirtualHostURLs(matchMakingProtocol)

	matchMakingExtProtocol := matchmakingext.NewProtocol()
	globals.SecureEndpoint.RegisterServiceProtocol(matchMakingExtProtocol)
	commonmatchmakingext.NewCommonProtocol(matchMakingExtProtocol).SetManager(globals.MatchmakingManager)

	rankingProtocol := ranking.NewProtocol()
	globals.SecureEndpoint.RegisterServiceProtocol(rankingProtocol)

	matchmakeExtensionProtocol := matchmakeextension.NewProtocol()
	globals.SecureEndpoint.RegisterServiceProtocol(matchmakeExtensionProtocol)
	commonMatchmakeExtensionProtocol := commonmatchmakeextension.NewCommonProtocol(matchmakeExtensionProtocol)
	commonMatchmakeExtensionProtocol.SetManager(globals.MatchmakingManager)

	// * エントリーしたイベントごとに、ランダム対戦の検索を分ける
	registerEventSearch(matchmakeExtensionProtocol)

	// * イベントの部屋には、フレンドでなくても入れる
	commonMatchmakeExtensionProtocol.CanJoinMatchmakeSession = func(manager *common_globals.MatchmakingManager, pid types.PID, matchmakeSession matchmakingtypes.MatchmakeSession) *nex.Error {
		if bool(matchmakeSession.OpenParticipation) && globals.IsEventRoomHost(uint32(matchmakeSession.OwnerPID)) {
			return nil
		}

		// * 試験用: サーバーが持ち主を演じる部屋
		if vhostOfRoom(uint32(matchmakeSession.ID)) != nil {
			return nil
		}

		return common_globals.CanJoinMatchmakeSession(manager, pid, matchmakeSession)
	}
	matchmakeExtensionProtocol.GetMyBlockList = local_matchmake_extension.GetMyBlockList

	messageDeliveryProtocol := messagedelivery.NewProtocol()
	globals.SecureEndpoint.RegisterServiceProtocol(messageDeliveryProtocol)
	commonmessagedelivery.NewCommonProtocol(messageDeliveryProtocol).SetManager(globals.MessagingManager)

	// * 試験用: イベントの誘いを回す。YW2_EVENT_BOTS があるときだけ
	registerEventInvites()

	utilityProtocol := utility.NewProtocol()
	globals.SecureEndpoint.RegisterServiceProtocol(utilityProtocol)
	commonutility.NewCommonProtocol(utilityProtocol).SetManager(globals.UtilityManager)

	commonMatchmakeExtensionProtocol.CleanupMatchmakeSessionSearchCriterias = func(searchCriterias types.List[matchmakingtypes.MatchmakeSessionSearchCriteria]) {
		for _, searchCriteria := range searchCriterias {
			searchCriteria.Attribs[2] = types.NewString("")
		}

	}

}
