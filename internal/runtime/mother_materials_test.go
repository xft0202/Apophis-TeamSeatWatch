package runtime

import (
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestParseMotherMaterialTXTReportsEveryLineWithoutSecrets(t *testing.T) {
	materials, rows := parseMotherMaterialTXT("one@example.com----pw----JBSWY3DPEHPK3PXP\nmalformed\nwaiting@example.com----pw----\n")
	if len(materials) != 3 || len(rows) != 3 {
		t.Fatalf("expected one result per physical line, got materials=%d rows=%d", len(materials), len(rows))
	}
	if rows[0].Status != ownerapi.MotherAccountImportRowStatusImported {
		t.Fatalf("valid row status = %q", rows[0].Status)
	}
	if rows[1].Status != ownerapi.MotherAccountImportRowStatusInvalid {
		t.Fatalf("malformed row status = %q", rows[1].Status)
	}
	if rows[2].Status != ownerapi.MotherAccountImportRowStatusNeedsTotp {
		t.Fatalf("missing TOTP row status = %q", rows[2].Status)
	}
	for _, row := range rows {
		if row.Message != nil && (containsSecret(*row.Message, "pw") || containsSecret(*row.Message, "JBSWY3D")) {
			t.Fatal("line feedback leaked sensitive material")
		}
	}
}

func TestParseMotherMaterialTXTMarksMalformedTotpAsRepairable(t *testing.T) {
	_, rows := parseMotherMaterialTXT("one@example.com----pw----not-base32")
	if len(rows) != 1 || rows[0].Status != ownerapi.MotherAccountImportRowStatusNeedsTotp {
		t.Fatalf("malformed TOTP must remain repairable, got %#v", rows)
	}
}

func containsSecret(value, secret string) bool {
	return len(secret) > 0 && len(value) >= len(secret) && stringIndex(value, secret) >= 0
}

func stringIndex(value, needle string) int {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}
