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

func TestParseChildMaterialsAndValidTargetInputSupport1024To16384Credentials(t *testing.T) {
	longCookie := strings.Repeat("s", 5200)
	maxCookie := strings.Repeat("m", 16384)
	overCookie := strings.Repeat("x", 16385)
	content := "child1@example.com----" + longCookie + "----cookie\n" +
		"child2@example.com----" + maxCookie + "----google\n" +
		"child3@example.com----" + overCookie + "----cookie\n"
	materials, rows := parseChildMaterials(content)
	if len(rows) != 3 || len(materials) != 3 {
		t.Fatalf("got rows=%d materials=%d", len(rows), len(materials))
	}
	if rows[0].Status != ownerapi.ChildMaterialsImportRowStatusImported || materials[0].password != longCookie {
		t.Fatalf("5200-char child cookie rejected: %+v", rows[0])
	}
	if rows[1].Status != ownerapi.ChildMaterialsImportRowStatusImported || materials[1].password != maxCookie {
		t.Fatalf("16384-char child cookie rejected: %+v", rows[1])
	}
	if rows[2].Status != ownerapi.ChildMaterialsImportRowStatusInvalid {
		t.Fatalf("16385-char child cookie should be invalid: %+v", rows[2])
	}
	if !validTargetInput("child1@example.com", "child1@example.com", longCookie, "cookie", "", "") {
		t.Fatal("validTargetInput rejected 5200-char credential")
	}
	if !validTargetInput("child1@example.com", "child1@example.com", maxCookie, "google", "", "") {
		t.Fatal("validTargetInput rejected 16384-char credential")
	}
	if validTargetInput("child1@example.com", "child1@example.com", overCookie, "cookie", "", "") {
		t.Fatal("validTargetInput accepted 16385-char credential")
	}
}
