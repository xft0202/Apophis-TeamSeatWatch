package runtime

import (
	"strings"
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

func TestParseMotherMaterialTXTAcceptsLongCookiesUpTo16384AndSessionMarkers(t *testing.T) {
	longCookie := strings.Repeat("c", 5200)
	maxCookie := strings.Repeat("m", 16384)
	overCookie := strings.Repeat("x", 16385)
	input := "one@example.com----" + longCookie + "----cookie\n" +
		"two@example.com----" + maxCookie + "----google\n" +
		"three@example.com----" + overCookie + "----cookie\n"
	materials, rows := parseMotherMaterialTXT(input)
	if len(rows) != 3 || len(materials) != 3 {
		t.Fatalf("got rows=%d materials=%d", len(rows), len(materials))
	}
	if rows[0].Status != ownerapi.MotherAccountImportRowStatusImported || materials[0].password != longCookie {
		t.Fatalf("5200-char cookie rejected: %+v", rows[0])
	}
	if rows[1].Status != ownerapi.MotherAccountImportRowStatusImported || materials[1].password != maxCookie {
		t.Fatalf("16384-char cookie rejected: %+v", rows[1])
	}
	if rows[2].Status != ownerapi.MotherAccountImportRowStatusInvalid {
		t.Fatalf("16385-char cookie should be invalid, got: %+v", rows[2])
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
