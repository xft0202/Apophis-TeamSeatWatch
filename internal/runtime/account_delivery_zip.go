package runtime

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/platform"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/thirtydayoauth"
)

// Convert only the authorized immutable account payload with the reference
// exporter. Using its publication time preserves expiry and repeat downloads;
// no credential refresh or live account material is involved.
func accountDeliveryZIP(payload []byte, created time.Time) ([]byte, error) {
	var source platform.DeliveryCredentialSet
	if err := json.Unmarshal(payload, &source); err != nil {
		return nil, err
	}
	identity := thirtydayoauth.DecodeIDToken(source.IDToken)
	if strings.TrimSpace(source.RefreshToken) == "" || source.WorkspaceID == "" || source.PlatformSubjectID == "" || identity.AccountID != source.WorkspaceID || identity.UserID != source.PlatformSubjectID {
		return nil, errors.New("original account delivery credentials incomplete")
	}
	entry, err := thirtydayoauth.BuildSub2APIAt(created, thirtydayoauth.BuildInput{
		WorkspaceID: source.WorkspaceID, RefreshToken: source.RefreshToken, AccessToken: source.AccessToken,
		IDToken: source.IDToken, ExpiresIn: source.ExpiresIn, IssuedAt: created, FirstOK: created, EmptyModelMapping: true,
	})
	if err != nil {
		return nil, err
	}
	data, err := thirtydayoauth.MarshalSub2APIBundle(created, []map[string]any{entry})
	if err != nil {
		return nil, err
	}
	defer clear(data)
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "sub2api_all.json", Method: zip.Deflate, Modified: created.UTC().Truncate(time.Second)}
	file, err := archive.CreateHeader(header)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
