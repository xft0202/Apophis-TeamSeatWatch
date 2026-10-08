package runtime

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestStandbyChildSelectionRejectsDuplicateAndChangedCount(t *testing.T) {
	id := uuid.New()
	selected := ownerapi.StandbyChildSelection{Scope: ownerapi.StandbyChildSelectionScopeSelected, Count: 1, Members: []ownerapi.StandbyChildSelectionMember{{AccountId: id, MembershipVersion: 0}}}
	if !validStandbySelection(selected, 1) {
		t.Fatal("one frozen account rejected")
	}
	selected.Members = append(selected.Members, selected.Members[0])
	selected.Count = 2
	if validStandbySelection(selected, 2) {
		t.Fatal("duplicate account would inflate count")
	}
	selected.Members = selected.Members[:1]
	selected.Count = 1
	if validStandbySelection(selected, 2) {
		t.Fatal("changed expected count accepted")
	}
}

func TestStandbyChildBatchAcceptsTenThousandFrozenMembers(t *testing.T) {
	members := make([]ownerapi.StandbyChildSelectionMember, 10000)
	for i := range members {
		members[i] = ownerapi.StandbyChildSelectionMember{AccountId: uuid.New(), MembershipVersion: 1}
	}
	body, _ := json.Marshal(ownerapi.StandbyChildBatchChange{Name: "large", Confirmed: true, ExpectedCount: len(members), Selection: ownerapi.StandbyChildSelection{Scope: ownerapi.StandbyChildSelectionScopeSelected, Count: len(members), Members: members}})
	if len(body) < 512<<10 {
		t.Fatalf("test request should exceed ordinary Owner JSON limit: %d", len(body))
	}
	request := httptest.NewRequest("POST", "/api/owner/v1/standby-child-batches", bytes.NewReader(body))
	var decoded ownerapi.StandbyChildBatchChange
	if !decodeStandbyChange(httptest.NewRecorder(), request, &decoded) || !validStandbySelection(decoded.Selection, 10000) {
		t.Fatal("10k frozen selection rejected")
	}
}
