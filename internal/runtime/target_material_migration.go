package runtime

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	targetdomain "github.com/xft0202/Apophis-TeamSeatWatch/internal/target"
)

// sealExistingTargetMaterials holds an exclusive table lock during the one-time
// conversion, so no worker can read old plaintext between conversion and startup.
func sealExistingTargetMaterials(ctx context.Context, pool *pgxpool.Pool, ring auth.KeyRing) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `LOCK TABLE tsw_target_credentials IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT target_account_id,password_secret,totp_secret,recovery_secret,material_status,materials_sealed FROM tsw_target_credentials`)
	if err != nil {
		return err
	}
	type record struct {
		id                       string
		password, totp, recovery []byte
		status                   string
		sealed                   bool
	}
	var records []record
	for rows.Next() {
		var row record
		if err = rows.Scan(&row.id, &row.password, &row.totp, &row.recovery, &row.status, &row.sealed); err != nil {
			break
		}
		records = append(records, row)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, row := range records {
		changed := false
		material := []*[]byte{&row.password, &row.totp, &row.recovery}
		for _, field := range material {
			if len(*field) == 0 {
				continue
			}
			if targetdomain.Sealed(*field) {
				if _, err = targetdomain.OpenMaterial(*field, ring); err != nil {
					return fmt.Errorf("target material key unavailable: %w", err)
				}
				continue
			}
			if row.sealed {
				return fmt.Errorf("sealed target credential has invalid ciphertext")
			}
			*field, err = targetdomain.SealMaterial(string(*field), ring)
			if err != nil {
				return err
			}
			changed = true
		}
		totp, err := targetdomain.OpenMaterial(row.totp, ring)
		if err != nil {
			return err
		}
		status := "needs_totp"
		if targetdomain.CompleteTOTP(totp) {
			status = "complete"
		}
		if !changed && row.status == status && row.sealed {
			continue
		}
		revision := "secret_revision"
		if changed {
			revision = "secret_revision+1"
		}
		_, err = tx.Exec(ctx, `UPDATE tsw_target_credentials SET password_secret=$2,totp_secret=$3,recovery_secret=$4,material_status=$5,materials_sealed=true,secret_revision=`+revision+`,version=version+1 WHERE target_account_id=$1`, row.id, row.password, row.totp, row.recovery, status)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
