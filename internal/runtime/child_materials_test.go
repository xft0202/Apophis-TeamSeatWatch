package runtime

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func TestDecodeChildImportJSONBoundsWholeRequestAndRejectsTrailingData(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"over legacy limit", `{"content":"` + strings.Repeat("x", 600<<10) + `"}`, true},
		{"over whole request limit", `{"content":"` + strings.Repeat("x", 10<<20) + `"}`, false},
		{"trailing JSON", `{"content":"valid"}{"content":"other"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/owner/v1/child-materials/import", strings.NewReader(tc.body))
			response := httptest.NewRecorder()
			var decoded ownerapi.ImportChildMaterialsJSONRequestBody
			if got := decodeChildImportJSON(response, request, &decoded); got != tc.want {
				t.Fatalf("decoded=%v want %v", got, tc.want)
			}
		})
	}
}

func TestParseChildMaterialsReportsEveryLineWithoutSecrets(t *testing.T) {
	materials, rows := parseChildMaterials("one@example.com----pw----JBSWY3DPEHPK3PXP\nmalformed\nwaiting@example.com----pw----\none@example.com----pw2----JBSWY3DPEHPK3PXP\n")
	if len(materials) != 4 || len(rows) != 4 {
		t.Fatalf("got materials=%d rows=%d", len(materials), len(rows))
	}
	want := []ownerapi.ChildMaterialsImportRowStatus{
		ownerapi.ChildMaterialsImportRowStatusImported,
		ownerapi.ChildMaterialsImportRowStatusInvalid,
		ownerapi.ChildMaterialsImportRowStatusNeedsTotp,
		ownerapi.ChildMaterialsImportRowStatusImported,
	}
	for i, row := range rows {
		if row.Status != want[i] {
			t.Fatalf("row %d status=%q", i+1, row.Status)
		}
	}
	for _, row := range rows {
		if row.Message != nil && (*row.Message == "pw" || *row.Message == "JBSWY3DPEHPK3PXP") {
			t.Fatal("line feedback leaked secret")
		}
	}
}
