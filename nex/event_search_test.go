package nex

import (
	"testing"

	"github.com/PretendoNetwork/nex-go/v2/types"
	matchmakingtypes "github.com/PretendoNetwork/nex-protocols-go/v2/match-making/types"
)

func TestApplyEventSlot(t *testing.T) {
	criteria := matchmakingtypes.NewMatchmakeSessionSearchCriteria()
	criteria.Attribs = types.List[types.String]{"", "0", "", "", "", ""}
	criterias := types.List[matchmakingtypes.MatchmakeSessionSearchCriteria]{criteria}
	attributes := types.List[types.UInt32]{0, 18, 0, 0, 0, 0}

	if !applyEventSlot(2, criterias, attributes) {
		t.Fatal("not applied")
	}
	if criterias[0].Attribs[5] != "2" || attributes[5] != 2 {
		t.Fatalf("%v %v", criterias[0].Attribs, attributes)
	}

	// * 他の属性は変えない
	if criterias[0].Attribs[1] != "0" || attributes[1] != 18 {
		t.Fatalf("changed: %v %v", criterias[0].Attribs, attributes)
	}

	short := types.List[types.UInt32]{0, 1}
	if applyEventSlot(1, criterias, short) {
		t.Fatal("short attributes")
	}
}
