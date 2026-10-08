package runtime

import (
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestValidDeliveryRecordFiltersRejectUnknownValues(t *testing.T) {
	service := ownerapi.DeliveryServiceFilter("unknown")
	if validDeliveryRecordFilters(ownerapi.ListDeliveryRecordsParams{ServiceStatus: &service}) {
		t.Fatal("unknown service filter was accepted")
	}

	card := ownerapi.DeliveryCardFilter("unknown")
	if validDeliveryRecordFilters(ownerapi.ListDeliveryRecordsParams{CardStatus: &card}) {
		t.Fatal("unknown card filter was accepted")
	}

	order := ownerapi.DeliveryOrderFilter("unknown")
	if validDeliveryRecordFilters(ownerapi.ListDeliveryRecordsParams{OrderStatus: &order}) {
		t.Fatal("unknown order filter was accepted")
	}

	state := ownerapi.CardState("unknown")
	if validDeliveryRecordFilters(ownerapi.ListDeliveryRecordsParams{CardState: &state}) {
		t.Fatal("unknown card state accepted")
	}
	valid := ownerapi.DeliveryServiceFilterActive
	if !validDeliveryRecordFilters(ownerapi.ListDeliveryRecordsParams{ServiceStatus: &valid}) {
		t.Fatal("known service filter was rejected")
	}
}
