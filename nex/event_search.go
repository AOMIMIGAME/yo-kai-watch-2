package nex

import (
	"fmt"
	"strconv"

	"github.com/PretendoNetwork/nex-go/v2"
	"github.com/PretendoNetwork/nex-go/v2/types"
	matchmakeextension "github.com/PretendoNetwork/nex-protocols-go/v2/matchmake-extension"
	matchmakingtypes "github.com/PretendoNetwork/nex-protocols-go/v2/match-making/types"
	"github.com/PretendoNetwork/yo-kai-watch-2/globals"
)

// * ゲームは 6 つの属性のうち、これを使っていない（どの対戦でも 0）
const eventSearchAttribute = 5

// * 全員の検索とエントリーの値を合わせる。普通の人は 0 なので、イベントの部屋には入らない
func applyEventSlot(slot uint32, criterias types.List[matchmakingtypes.MatchmakeSessionSearchCriteria], attributes types.List[types.UInt32]) bool {
	if len(attributes) <= eventSearchAttribute {
		return false
	}

	for _, criteria := range criterias {
		if len(criteria.Attribs) <= eventSearchAttribute {
			return false
		}
	}

	attributes[eventSearchAttribute] = types.UInt32(slot)
	for _, criteria := range criterias {
		criteria.Attribs[eventSearchAttribute] = types.String(strconv.FormatUint(uint64(slot), 10))
	}

	return true
}

func registerEventSearch(protocol *matchmakeextension.Protocol) {
	if len(globals.EventBots()) == 0 {
		return
	}

	original := protocol.AutoMatchmakeWithSearchCriteriaPostpone

	protocol.SetHandlerAutoMatchmakeWithSearchCriteriaPostpone(func(err error, packet nex.PacketInterface, callID uint32, lstSearchCriteria types.List[matchmakingtypes.MatchmakeSessionSearchCriteria], anyGathering matchmakingtypes.GatheringHolder, strMessage types.String) (*nex.RMCMessage, *nex.Error) {
		if err == nil {
			if session, ok := anyGathering.Object.(matchmakingtypes.MatchmakeSession); ok {
				pid := uint32(packet.Sender().PID())
				slot := globals.EventSlot(pid)

				if !applyEventSlot(slot, lstSearchCriteria, session.Attributes) {
					fmt.Printf("[YW2-EVENT] pid=%d search has no event attribute\n", pid)
				} else if slot != 0 {
					fmt.Printf("[YW2-EVENT] pid=%d searches in event slot %d\n", pid, slot)
				}

				anyGathering.Object = session
			}
		}

		return original(err, packet, callID, lstSearchCriteria, anyGathering, strMessage)
	})
}
