package runtime

import (
	"archive/zip"
	"bytes"
	"time"
)

// Wrap the authorized immutable account payload without rebuilding credentials
// from live account materials. The version time makes repeat downloads stable.
func accountDeliveryZIP(payload []byte, created time.Time) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "apophis-teamseatwatch-delivery.json", Method: zip.Deflate, Modified: created.UTC().Truncate(time.Second)}
	file, err := archive.CreateHeader(header)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(payload); err != nil {
		return nil, err
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
