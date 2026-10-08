package runtime

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/thirtydayoauth"
)

func accountDeliveryFixture(t *testing.T, created time.Time, workspace, subject string) []byte {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"exp": created.Add(time.Hour).Unix(), "email": "target@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": workspace, "chatgpt_user_id": subject, "chatgpt_plan_type": "team",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".mock"
	payload, err := json.Marshal(platform.DeliveryCredentialSet{
		AccessToken: token, RefreshToken: "original-refresh", IDToken: token,
		WorkspaceID: workspace, PlatformSubjectID: subject, ExpiresIn: 3600, Scope: platform.CodexScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func accountDeliveryBundle(t *testing.T, data []byte) map[string]any {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 || archive.File[0].Name != "sub2api_all.json" {
		t.Fatal("account ZIP must contain the reference Sub2API JSON")
	}
	file, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	payload, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(payload, &bundle); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestAccountDeliveryZIPUsesReferenceSub2APIFormat(t *testing.T) {
	// Original downloads must remain available after the access token expires.
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	payload := accountDeliveryFixture(t, created, "workspace-card", "subject")
	original := bytes.Clone(payload)
	data, err := accountDeliveryZIP(payload, created)
	if err != nil {
		t.Fatal(err)
	}
	bundle := accountDeliveryBundle(t, data)
	if len(bundle) != 3 || bundle["exported_at"] != "2026-01-02T03:04:05Z" {
		t.Fatal("reference bundle header changed")
	}
	proxies, ok := bundle["proxies"].([]any)
	if !ok || len(proxies) != 0 {
		t.Fatal("Sub2API requires a non-null proxies array")
	}
	accounts, ok := bundle["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatal("one card must export one Sub2API account")
	}
	var source platform.DeliveryCredentialSet
	if err := json.Unmarshal(payload, &source); err != nil {
		t.Fatal(err)
	}
	want, err := thirtydayoauth.BuildSub2APIAt(created, thirtydayoauth.BuildInput{
		WorkspaceID: source.WorkspaceID, AccessToken: source.AccessToken, RefreshToken: source.RefreshToken,
		IDToken: source.IDToken, ExpiresIn: source.ExpiresIn, IssuedAt: created, FirstOK: created, EmptyModelMapping: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(wantJSON, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accounts[0], expected) {
		t.Fatal("account export must reuse the reference OAuth mapping")
	}
	credentials := accounts[0].(map[string]any)["credentials"].(map[string]any)
	if credentials["access_token"] != source.AccessToken || credentials["refresh_token"] != source.RefreshToken || credentials["id_token"] != source.IDToken || credentials["expires_at"] != float64(created.Add(time.Hour).Unix()) {
		t.Fatal("original credentials or absolute expiry changed")
	}
	repeated, err := accountDeliveryZIP(payload, created.In(time.FixedZone("UTC+8", 8*3600)))
	if err != nil || !bytes.Equal(data, repeated) || !bytes.Equal(payload, original) {
		t.Fatal("repeat downloads must be stable and leave the immutable payload untouched")
	}
}

func TestAccountDeliveryZIPRejectsInvalidOriginal(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, mode := range []string{"json", "empty", "refresh", "id", "workspace", "subject", "access"} {
		t.Run(mode, func(t *testing.T) {
			payload := accountDeliveryFixture(t, created, "workspace-card", "subject")
			var source map[string]any
			if err := json.Unmarshal(payload, &source); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "json":
				payload = []byte("{")
			case "empty":
				payload = []byte("{}")
			default:
				key := map[string]string{"refresh": "refresh_token", "id": "id_token", "workspace": "workspace_id", "subject": "platform_subject_id", "access": "access_token"}[mode]
				source[key] = "invalid"
				if mode == "refresh" {
					source[key] = ""
				}
				var err error
				payload, err = json.Marshal(source)
				if err != nil {
					t.Fatal(err)
				}
			}
			if data, err := accountDeliveryZIP(payload, created); err == nil || data != nil {
				t.Fatal("invalid original must not produce a downloadable ZIP")
			}
		})
	}
}
