package runtime

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/auth"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

// DestinationProbe validates only the configured channel connection and target
// scope. It must not perform a delivery or receive customer credentials.
type DestinationProbe interface {
	ProbeDestination(ctx context.Context, endpoint, targetGroup, secret string) (connection ownerapi.DeliveryDestinationTestConnection, target ownerapi.DeliveryDestinationTestTarget)
}

func (h *OwnerAuthHandler) listDeliveryDestinations(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	rows, err := h.pool.Query(r.Context(), `SELECT destination.id,destination.name,destination.endpoint,destination.target_group,destination.enabled,destination.secret_ciphertext IS NOT NULL,destination.revision,destination.test_connection,destination.test_target,destination.test_revision,destination.tested_at,selection.destination_id IS NOT NULL FROM tsw_delivery_destinations destination LEFT JOIN tsw_delivery_destination_selection selection ON selection.destination_id=destination.id ORDER BY destination.created_at,destination.id`)
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]ownerapi.DeliveryDestination, 0)
	for rows.Next() {
		item, err := scanDeliveryDestination(rows)
		if err != nil {
			h.destinationFailure(w, r, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ownerapi.DeliveryDestinationList{Items: items})
}

func (h *OwnerAuthHandler) createDeliveryDestination(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.CreateDeliveryDestinationJSONRequestBody
	if !decodeJSON(w, r, &request) {
		h.destinationInvalid(w, r, owner, "create")
		return
	}
	name, endpoint, targetGroup, valid := validateDestination(request.Name, request.Endpoint, request.TargetGroup, request.Secret)
	if !valid {
		h.destinationInvalid(w, r, owner, "create")
		return
	}
	plaintext := []byte(request.Secret)
	version, nonce, ciphertext, err := auth.EncryptSecret(plaintext, h.keyRing)
	clear(plaintext)
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	var item ownerapi.DeliveryDestination
	err = h.pool.QueryRow(r.Context(), `INSERT INTO tsw_delivery_destinations(name,endpoint,target_group,secret_key_version,secret_nonce,secret_ciphertext) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id,name,endpoint,target_group,enabled,true,revision,false`, name, endpoint, targetGroup, version, nonce, ciphertext).Scan(&item.Id, &item.Name, &item.Endpoint, &item.TargetGroup, &item.Enabled, &item.HasSecret, &item.Revision, &item.Selected)
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	item.Test = nil
	_ = owner
	writeJSON(w, http.StatusCreated, item)
}

func (h *OwnerAuthHandler) updateDeliveryDestination(w http.ResponseWriter, r *http.Request, destinationID openapi_types.UUID) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	var request ownerapi.UpdateDeliveryDestinationJSONRequestBody
	if !decodeJSON(w, r, &request) {
		h.destinationInvalid(w, r, owner, "update")
		return
	}
	name, endpoint, targetGroup, valid := validateDestination(request.Name, request.Endpoint, request.TargetGroup, "x")
	if !valid {
		h.destinationInvalid(w, r, owner, "update")
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var version uint16
	var nonce, ciphertext []byte
	err = tx.QueryRow(r.Context(), `SELECT secret_key_version,secret_nonce,secret_ciphertext FROM tsw_delivery_destinations WHERE id=$1 FOR UPDATE`, destinationID).Scan(&version, &nonce, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		h.destinationNotFound(w, r, owner, "update")
		return
	}
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	if request.Secret != nil && strings.TrimSpace(*request.Secret) != "" {
		plaintext := []byte(*request.Secret)
		version, nonce, ciphertext, err = auth.EncryptSecret(plaintext, h.keyRing)
		clear(plaintext)
		if err != nil {
			h.destinationFailure(w, r, err)
			return
		}
	} else if request.Secret != nil && *request.Secret != "" {
		h.destinationInvalid(w, r, owner, "update")
		return
	}
	var item ownerapi.DeliveryDestination
	err = tx.QueryRow(r.Context(), `UPDATE tsw_delivery_destinations SET name=$2,endpoint=$3,target_group=$4,enabled=$5,secret_key_version=$6,secret_nonce=$7,secret_ciphertext=$8,revision=revision+1,test_connection=NULL,test_target=NULL,test_revision=NULL,tested_at=NULL,updated_at=now() WHERE id=$1 RETURNING id,name,endpoint,target_group,enabled,true,revision,false`, destinationID, name, endpoint, targetGroup, request.Enabled, version, nonce, ciphertext).Scan(&item.Id, &item.Name, &item.Endpoint, &item.TargetGroup, &item.Enabled, &item.HasSecret, &item.Revision, &item.Selected)
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM tsw_delivery_destination_selection WHERE destination_id=$1`, destinationID); err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	item.Test = nil
	writeJSON(w, http.StatusOK, item)
}

func (h *OwnerAuthHandler) testDeliveryDestination(w http.ResponseWriter, r *http.Request, destinationID openapi_types.UUID) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	probe := h.destinationProbe
	if probe == nil {
		probe = NewSub2APIProbe(Sub2APIProbeConfig{})
	}
	var endpoint, targetGroup string
	var version uint16
	var nonce, ciphertext []byte
	var enabled bool
	var revision int64
	err := h.pool.QueryRow(r.Context(), `SELECT endpoint,target_group,secret_key_version,secret_nonce,secret_ciphertext,enabled,revision FROM tsw_delivery_destinations WHERE id=$1`, destinationID).Scan(&endpoint, &targetGroup, &version, &nonce, &ciphertext, &enabled, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		h.destinationNotFound(w, r, owner, "test")
		return
	}
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	if !enabled {
		h.destinationProblem(w, r, owner, "test", http.StatusConflict, "destination_disabled", "The destination is disabled")
		return
	}
	secret, err := auth.DecryptSecret(version, nonce, ciphertext, h.keyRing)
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	connection, target := probe.ProbeDestination(r.Context(), endpoint, targetGroup, string(secret))
	clear(secret)
	if !connection.Valid() || !target.Valid() || connection != ownerapi.DeliveryDestinationTestConnectionConnected && target == ownerapi.DeliveryDestinationTestTargetConnected {
		connection, target = ownerapi.DeliveryDestinationTestConnectionConnectionFailed, ownerapi.DeliveryDestinationTestTargetUntested
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var item ownerapi.DeliveryDestination
	var testedAt time.Time
	err = tx.QueryRow(r.Context(), `UPDATE tsw_delivery_destinations SET test_connection=$2,test_target=$3,test_revision=revision,tested_at=now(),updated_at=now() WHERE id=$1 AND revision=$4 AND enabled RETURNING id,name,endpoint,target_group,enabled,true,revision,tested_at`, destinationID, connection, target, revision).Scan(&item.Id, &item.Name, &item.Endpoint, &item.TargetGroup, &item.Enabled, &item.HasSecret, &item.Revision, &testedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		h.destinationProblem(w, r, owner, "test", http.StatusConflict, "test_stale", "The destination changed while it was being tested; test it again")
		return
	}
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	if connection != ownerapi.DeliveryDestinationTestConnectionConnected || target != ownerapi.DeliveryDestinationTestTargetConnected {
		_, err = tx.Exec(r.Context(), `DELETE FROM tsw_delivery_destination_selection WHERE destination_id=$1`, destinationID)
		if err != nil {
			h.destinationFailure(w, r, err)
			return
		}
	}
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tsw_delivery_destination_selection WHERE destination_id=$1)`, destinationID).Scan(&item.Selected)
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	item.Test = &ownerapi.DeliveryDestinationTest{Connection: connection, Target: target, Revision: revision, TestedAt: testedAt}
	writeJSON(w, http.StatusOK, item)
}

func (h *OwnerAuthHandler) selectDeliveryDestination(w http.ResponseWriter, r *http.Request, destinationID openapi_types.UUID) {
	owner, ok := h.authenticated(w, r, true)
	if !ok {
		return
	}
	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var enabled bool
	var connection, target *string
	var revision, testRevision int64
	err = tx.QueryRow(r.Context(), `SELECT enabled,test_connection,test_target,revision,COALESCE(test_revision,0) FROM tsw_delivery_destinations WHERE id=$1 FOR UPDATE`, destinationID).Scan(&enabled, &connection, &target, &revision, &testRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		h.destinationNotFound(w, r, owner, "select")
		return
	}
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	if !enabled || connection == nil || target == nil || *connection != "connected" || *target != "connected" || testRevision != revision {
		h.destinationProblem(w, r, owner, "select", http.StatusConflict, "destination_not_selectable", "The destination must be enabled and pass a current connection and target test")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO tsw_delivery_destination_selection(singleton,destination_id) VALUES (true,$1) ON CONFLICT (singleton) DO UPDATE SET destination_id=EXCLUDED.destination_id,selected_at=now()`, destinationID); err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	var item ownerapi.DeliveryDestination
	var testedAt time.Time
	err = tx.QueryRow(r.Context(), `SELECT id,name,endpoint,target_group,enabled,true,revision,true,test_connection,test_target,test_revision,tested_at FROM tsw_delivery_destinations WHERE id=$1`, destinationID).Scan(&item.Id, &item.Name, &item.Endpoint, &item.TargetGroup, &item.Enabled, &item.HasSecret, &item.Revision, &item.Selected, &connection, &target, &testRevision, &testedAt)
	if err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		h.destinationFailure(w, r, err)
		return
	}
	item.Test = &ownerapi.DeliveryDestinationTest{Connection: ownerapi.DeliveryDestinationTestConnection(*connection), Target: ownerapi.DeliveryDestinationTestTarget(*target), Revision: testRevision, TestedAt: testedAt}
	writeJSON(w, http.StatusOK, item)
}

func validateDestination(name, endpoint, targetGroup, secret string) (string, string, string, bool) {
	name, endpoint, targetGroup = strings.TrimSpace(name), strings.TrimSpace(endpoint), strings.TrimSpace(targetGroup)
	_, validURL := validSub2APIBase(endpoint)
	groupID, err := strconv.ParseInt(targetGroup, 10, 64)
	return name, endpoint, targetGroup, validURL && err == nil && groupID > 0 && strconv.FormatInt(groupID, 10) == targetGroup && validLength(name, 1, 120) && strings.TrimSpace(secret) != "" && validLength(secret, 1, 4096)
}

func scanDeliveryDestination(row pgx.Row) (ownerapi.DeliveryDestination, error) {
	var item ownerapi.DeliveryDestination
	var connection, target *string
	var testRevision *int64
	var testedAt *time.Time
	err := row.Scan(&item.Id, &item.Name, &item.Endpoint, &item.TargetGroup, &item.Enabled, &item.HasSecret, &item.Revision, &connection, &target, &testRevision, &testedAt, &item.Selected)
	if err != nil {
		return item, err
	}
	if connection != nil && target != nil && testRevision != nil && testedAt != nil {
		item.Test = &ownerapi.DeliveryDestinationTest{Connection: ownerapi.DeliveryDestinationTestConnection(*connection), Target: ownerapi.DeliveryDestinationTestTarget(*target), Revision: *testRevision, TestedAt: *testedAt}
	}
	return item, nil
}

func (h *OwnerAuthHandler) destinationInvalid(w http.ResponseWriter, r *http.Request, owner ownerContext, operation string) {
	h.rejectOwnerMutation(w, r, owner, "delivery_destination."+operation, "invalid_request", http.StatusUnprocessableEntity, "invalid_destination", "Invalid Destination", "Destination fields are invalid")
}
func (h *OwnerAuthHandler) destinationNotFound(w http.ResponseWriter, r *http.Request, owner ownerContext, operation string) {
	h.rejectOwnerMutation(w, r, owner, "delivery_destination."+operation, "target_not_found", http.StatusNotFound, "destination_not_found", "Not Found", "The delivery destination was not found")
}
func (h *OwnerAuthHandler) destinationProblem(w http.ResponseWriter, r *http.Request, owner ownerContext, operation string, status int, code, detail string) {
	h.rejectOwnerMutation(w, r, owner, "delivery_destination."+operation, "conflict", status, code, "Conflict", detail)
}
func (h *OwnerAuthHandler) destinationFailure(w http.ResponseWriter, r *http.Request, err error) {
	h.workspaceFailure(w, r, err)
}
