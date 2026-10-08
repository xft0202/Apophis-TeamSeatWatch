package platform

// InvitationCapacity keeps paid seats, actual occupants and outstanding
// invitations separate. An invitation reserves a future seat, not membership.
type InvitationCapacity struct {
	Opened, Occupied, Reserved, Remaining int
	Known                                 bool
}

func PremiumInvitationCapacity(entitlements, occupants map[string]int, reserved int) InvitationCapacity {
	opened, openedKnown := entitlements["prolite"]
	occupied, occupiedKnown := occupants["prolite"]
	capacity := InvitationCapacity{Opened: opened, Occupied: occupied, Reserved: reserved}
	if !openedKnown || !occupiedKnown || opened < 0 || occupied < 0 || reserved < 0 {
		return capacity
	}
	capacity.Known = true
	capacity.Remaining = max(0, opened-occupied-reserved)
	return capacity
}
