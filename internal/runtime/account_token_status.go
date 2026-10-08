package runtime

import "github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"

// These saved sources have distinct scopes. Personal login stores only an AT;
// validated OAuth components contain both AT and RT. Presence never asserts
// that a token is still valid, and no secret leaves the database projection.
const targetListTokensSQL = ` LEFT JOIN LATERAL (
 SELECT COALESCE(bool_or(saved.has_at),false) AS has_at,
 COALESCE(bool_or(saved.has_rt),false) AS has_rt
 FROM (
  SELECT true AS has_at,false AS has_rt FROM tsw_target_personal_sessions session
  WHERE session.target_account_id=target.id AND session.secret_revision=credentials.secret_revision
  UNION ALL
  SELECT jsonb_typeof(version.payload->'access_token')='string' AND length(btrim(version.payload->>'access_token'))>0,
   jsonb_typeof(version.payload->'refresh_token')='string' AND length(btrim(version.payload->>'refresh_token'))>0
  FROM tsw_batch_memberships membership JOIN tsw_oauth_assets asset ON asset.membership_id=membership.id
  JOIN tsw_delivery_versions version ON version.id=asset.current_delivery_version_id AND version.oauth_asset_id=asset.id
  WHERE membership.target_account_id=target.id
  UNION ALL
  SELECT true,component.kind='oauth' FROM tsw_rotation_join_credential_attempts attempt
  JOIN tsw_rotation_join_credential_components component ON component.attempt_id=attempt.attempt_id
  WHERE attempt.candidate_account_id=target.id AND attempt.secret_revision=credentials.secret_revision
 ) saved
) tokens ON true `

const targetTokenFilterSQL = ` AND ($5='' OR ($5='has_at' AND tokens.has_at)
 OR ($5='missing_at' AND NOT tokens.has_at) OR ($5='has_rt' AND tokens.has_rt)
 OR ($5='missing_rt' AND NOT tokens.has_rt) OR ($5='both' AND tokens.has_at AND tokens.has_rt)
 OR ($5='none' AND NOT tokens.has_at AND NOT tokens.has_rt)) `

func validTargetTokenFilter(value string) bool {
	switch value {
	case "", "has_at", "missing_at", "has_rt", "missing_rt", "both", "none":
		return true
	default:
		return false
	}
}

type rowWithTokenStatus struct {
	scanner interface{ Scan(...any) error }
	tokens  *ownerapi.TargetAccountTokenStatus
}

func (row rowWithTokenStatus) Scan(values ...any) error {
	return row.scanner.Scan(append(values, &row.tokens.HasAccessToken, &row.tokens.HasRefreshToken)...)
}
