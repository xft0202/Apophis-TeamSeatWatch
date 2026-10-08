package thirtydayoauth

import (
	"encoding/json"
	"time"
)

// MarshalSub2APIBundle uses the reference export envelope for both account-card
// downloads and batch archives. Empty proxies must be an array, never null.
func MarshalSub2APIBundle(exportedAt time.Time, accounts []map[string]any) ([]byte, error) {
	return json.MarshalIndent(struct {
		ExportedAt string           `json:"exported_at"`
		Proxies    []any            `json:"proxies"`
		Accounts   []map[string]any `json:"accounts"`
	}{exportedAt.UTC().Truncate(time.Second).Format(time.RFC3339), []any{}, accounts}, "", "  ")
}
