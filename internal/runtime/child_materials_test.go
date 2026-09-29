package runtime

import (
	"testing"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

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
