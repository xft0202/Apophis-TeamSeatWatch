package runtime

import "github.com/google/uuid"

func validStandbyMembershipFilter(value string) bool {
	return value == "" || value == "unassigned" || value == "assigned"
}

type rowWithStandbyStatus struct {
	rowWithTokenStatus
	batchID   **uuid.UUID
	batchName **string
}

func (row rowWithStandbyStatus) Scan(values ...any) error {
	return row.scanner.Scan(append(values, &row.tokens.HasAccessToken, &row.tokens.HasRefreshToken, row.batchID, row.batchName)...)
}
