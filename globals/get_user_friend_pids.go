package globals

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	pbfriends "github.com/PretendoNetwork/grpc/go/friends"
	"github.com/PretendoNetwork/nex-protocols-go/v2/globals"
	"google.golang.org/grpc/metadata"
)

// * イベント用の偽のプレイヤー。登録がなくても、全員のフレンドとして応答に入れる
var eventBotPIDs = parseEventBotPIDs(os.Getenv("YW2_EVENT_BOTS"))

func parseEventBotPIDs(value string) []uint32 {
	pids := make([]uint32, 0)

	for _, text := range strings.Split(value, ",") {
		pid, err := strconv.ParseUint(strings.TrimSpace(text), 10, 32)
		if err == nil {
			pids = append(pids, uint32(pid))
		}
	}

	return pids
}

func addEventBots(pid uint32, friends []uint32) []uint32 {
	for _, bot := range eventBotPIDs {
		found := false
		for _, friend := range friends {
			if friend == bot {
				found = true
				break
			}
		}

		if !found {
			friends = append(friends, bot)
		}
	}

	if len(eventBotPIDs) > 0 {
		fmt.Printf("[YW2-FRIEND] friend list for pid=%d: %v\n", pid, friends)
	}

	return friends
}

func GetUserFriendPIDs(pid uint32) []uint32 {
	ctx := metadata.NewOutgoingContext(context.Background(), GRPCFriendsCommonMetadata)

	response, err := GRPCFriendsClient.GetUserFriendPIDs(ctx, &pbfriends.GetUserFriendPIDsRequest{Pid: pid})
	if err != nil {
		globals.Logger.Error(err.Error())
		return addEventBots(pid, make([]uint32, 0))
	}

	return addEventBots(pid, response.Pids)
}
