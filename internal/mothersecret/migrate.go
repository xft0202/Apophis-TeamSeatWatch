package mothersecret

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
)

type storedRow struct {
	id             uuid.UUID
	password, totp []byte
	revision       int64
}

// Migrate takes an exclusive table lock before inspecting any row. The sealed
// marker commits atomically with all replacements; once present, malformed or
// unsealed material is never interpreted as legacy plaintext again.
func Migrate(ctx context.Context, pool *pgxpool.Pool, ring auth.KeyRing) error {
	if pool == nil || ring == nil {
		return errors.New("mother material migration key/pool missing")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `LOCK TABLE tsw_mother_account_credentials IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	var migrated bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tsw_mother_material_crypto_state WHERE id=true)`).Scan(&migrated); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT mother_account_id,password_secret,totp_secret,secret_revision FROM tsw_mother_account_credentials ORDER BY mother_account_id`)
	if err != nil {
		return err
	}
	var all []storedRow
	for rows.Next() {
		var row storedRow
		if err = rows.Scan(&row.id, &row.password, &row.totp, &row.revision); err != nil {
			rows.Close()
			return err
		}
		all = append(all, row)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, row := range all {
		password, totp := row.password, row.totp
		passwordSealed := IsSealed(password)
		totpSealed := totp == nil || IsSealed(totp)
		if migrated && (!passwordSealed || !totpSealed) {
			return errors.New("unsealed mother material after migration")
		}
		if passwordSealed {
			password, err = Open(ring, row.id, row.revision, Password, password)
			if err != nil {
				return err
			}
		}
		if totp != nil && totpSealed {
			totp, err = Open(ring, row.id, row.revision, TOTP, totp)
			if err != nil {
				return err
			}
		}
		if passwordSealed && totpSealed {
			continue
		}
		nextRevision := row.revision + 1
		if nextRevision <= row.revision {
			return errors.New("mother material revision overflow")
		}
		sealedPassword, sealErr := Seal(ring, row.id, nextRevision, Password, password)
		if sealErr != nil {
			return sealErr
		}
		var sealedTOTP []byte
		if totp != nil {
			sealedTOTP, sealErr = Seal(ring, row.id, nextRevision, TOTP, totp)
			if sealErr != nil {
				return sealErr
			}
		}
		// A source revision change also invalidates any earlier Personal generation.
		for _, table := range []string{"tsw_mother_personal_sessions", "tsw_mother_workspace_visibility", "tsw_mother_discoveries", "tsw_mother_personal_access"} {
			if _, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE mother_account_id=$1`, row.id); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE tsw_mother_account_credentials SET password_secret=$2,totp_secret=$3,secret_revision=$4,version=version+1 WHERE mother_account_id=$1`, row.id, sealedPassword, sealedTOTP, nextRevision); err != nil {
			return err
		}
	}
	if !migrated {
		if _, err = tx.Exec(ctx, `INSERT INTO tsw_mother_material_crypto_state(id) VALUES (true)`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
