package runtime

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func (h *OwnerAuthHandler) ListAuditEvents(w http.ResponseWriter, r *http.Request, params ownerapi.ListAuditEventsParams) {
	h.listAuditEvents(w, r, params)
}

func (h *OwnerAuthHandler) ExportAuditEvents(w http.ResponseWriter, r *http.Request, params ownerapi.ExportAuditEventsParams) {
	h.exportAuditEvents(w, r, params)
}

func (h *OwnerAuthHandler) GetDataProtectionStatus(w http.ResponseWriter, r *http.Request) {
	h.getDataProtectionStatus(w, r)
}

func (h *OwnerAuthHandler) OpenRecoveryGate(w http.ResponseWriter, r *http.Request, params ownerapi.OpenRecoveryGateParams) {
	h.openRecoveryGate(w, r, params)
}

func (h *OwnerAuthHandler) ListMotherAccounts(w http.ResponseWriter, r *http.Request, params ownerapi.ListMotherAccountsParams) {
	h.listMotherAccounts(w, r, params)
}

func (h *OwnerAuthHandler) ListRedemptionRecords(w http.ResponseWriter, r *http.Request, params ownerapi.ListRedemptionRecordsParams) {
	h.listRedemptionRecords(w, r, params)
}

func (h *OwnerAuthHandler) ListRedemptionSourceBatches(w http.ResponseWriter, r *http.Request, params ownerapi.ListRedemptionSourceBatchesParams) {
	h.listRedemptionSourceBatches(w, r, params)
}

func (h *OwnerAuthHandler) GetRedemptionRecord(w http.ResponseWriter, r *http.Request, membershipID openapi_types.UUID, params ownerapi.GetRedemptionRecordParams) {
	h.getRedemptionRecord(w, r, membershipID, params)
}

func (h *OwnerAuthHandler) ListDeliveryDestinations(w http.ResponseWriter, r *http.Request) {
	h.listDeliveryDestinations(w, r)
}
func (h *OwnerAuthHandler) CreateDeliveryDestination(w http.ResponseWriter, r *http.Request, _ ownerapi.CreateDeliveryDestinationParams) {
	h.createDeliveryDestination(w, r)
}
func (h *OwnerAuthHandler) UpdateDeliveryDestination(w http.ResponseWriter, r *http.Request, destinationID openapi_types.UUID, _ ownerapi.UpdateDeliveryDestinationParams) {
	h.updateDeliveryDestination(w, r, destinationID)
}
func (h *OwnerAuthHandler) TestDeliveryDestination(w http.ResponseWriter, r *http.Request, destinationID openapi_types.UUID, _ ownerapi.TestDeliveryDestinationParams) {
	h.testDeliveryDestination(w, r, destinationID)
}
func (h *OwnerAuthHandler) SelectDeliveryDestination(w http.ResponseWriter, r *http.Request, destinationID openapi_types.UUID, _ ownerapi.SelectDeliveryDestinationParams) {
	h.selectDeliveryDestination(w, r, destinationID)
}

func (h *OwnerAuthHandler) CreateMotherAccount(w http.ResponseWriter, r *http.Request, _ ownerapi.CreateMotherAccountParams) {
	h.createMotherAccount(w, r)
}

func (h *OwnerAuthHandler) ImportMotherAccounts(w http.ResponseWriter, r *http.Request, params ownerapi.ImportMotherAccountsParams) {
	h.importMotherAccounts(w, r, params)
}

func (h *OwnerAuthHandler) ExportMotherAccounts(w http.ResponseWriter, r *http.Request, params ownerapi.ExportMotherAccountsParams) {
	h.exportMotherAccounts(w, r, params)
}

func (h *OwnerAuthHandler) UpdateMotherAccount(w http.ResponseWriter, r *http.Request, accountID openapi_types.UUID, params ownerapi.UpdateMotherAccountParams) {
	r.SetPathValue("accountId", accountID.String())
	h.updateMotherAccount(w, r, params)
}

func (h *OwnerAuthHandler) ListWorkspaces(w http.ResponseWriter, r *http.Request, params ownerapi.ListWorkspacesParams) {
	h.listWorkspaces(w, r, params)
}

func (h *OwnerAuthHandler) CreateWorkspace(w http.ResponseWriter, r *http.Request, _ ownerapi.CreateWorkspaceParams) {
	h.createWorkspace(w, r)
}

func (h *OwnerAuthHandler) ListWorkspacesNeedingAttention(w http.ResponseWriter, r *http.Request, params ownerapi.ListWorkspacesNeedingAttentionParams) {
	h.listNeedsAttention(w, r, params)
}

func (h *OwnerAuthHandler) GetWorkspace(w http.ResponseWriter, r *http.Request, workspaceID openapi_types.UUID, params ownerapi.GetWorkspaceParams) {
	r.SetPathValue("workspaceId", workspaceID.String())
	h.getWorkspace(w, r, params)
}

func (h *OwnerAuthHandler) UpdateWorkspace(w http.ResponseWriter, r *http.Request, workspaceID openapi_types.UUID, params ownerapi.UpdateWorkspaceParams) {
	r.SetPathValue("workspaceId", workspaceID.String())
	h.updateWorkspace(w, r, params)
}

func (h *OwnerAuthHandler) CreateMotherWorkspaceBinding(w http.ResponseWriter, r *http.Request, _ ownerapi.CreateMotherWorkspaceBindingParams) {
	h.createBinding(w, r)
}

func (h *OwnerAuthHandler) RefreshWorkspaceFacts(w http.ResponseWriter, r *http.Request, workspaceID openapi_types.UUID, _ ownerapi.RefreshWorkspaceFactsParams) {
	r.SetPathValue("workspaceId", workspaceID.String())
	h.refreshWorkspace(w, r)
}

func (h *OwnerAuthHandler) GetWorkspaceReadStatus(w http.ResponseWriter, r *http.Request, readID openapi_types.UUID) {
	r.SetPathValue("readId", readID.String())
	h.getWorkspaceRead(w, r)
}

func (h *OwnerAuthHandler) CreateWorkspaceManualVerification(w http.ResponseWriter, r *http.Request, workspaceID openapi_types.UUID, _ ownerapi.CreateWorkspaceManualVerificationParams) {
	r.SetPathValue("workspaceId", workspaceID.String())
	h.manualVerifyWorkspace(w, r)
}

func (h *OwnerAuthHandler) ListTargetAccounts(w http.ResponseWriter, r *http.Request, params ownerapi.ListTargetAccountsParams) {
	h.listTargetAccounts(w, r, params)
}

func (h *OwnerAuthHandler) CreateTargetAccount(w http.ResponseWriter, r *http.Request, _ ownerapi.CreateTargetAccountParams) {
	h.createTargetAccount(w, r)
}

func (h *OwnerAuthHandler) GetTargetAccount(w http.ResponseWriter, r *http.Request, targetAccountID openapi_types.UUID) {
	r.SetPathValue("targetAccountId", targetAccountID.String())
	h.getTargetAccount(w, r)
}

func (h *OwnerAuthHandler) UpdateTargetAccount(w http.ResponseWriter, r *http.Request, targetAccountID openapi_types.UUID, params ownerapi.UpdateTargetAccountParams) {
	r.SetPathValue("targetAccountId", targetAccountID.String())
	h.updateTargetAccount(w, r, params)
}

func (h *OwnerAuthHandler) PreviewTargetAccountImport(w http.ResponseWriter, r *http.Request, _ ownerapi.PreviewTargetAccountImportParams) {
	h.previewTargetImport(w, r, false)
}

func (h *OwnerAuthHandler) ImportTargetAccounts(w http.ResponseWriter, r *http.Request, _ ownerapi.ImportTargetAccountsParams) {
	h.previewTargetImport(w, r, true)
}

func (h *OwnerAuthHandler) CreateTargetAccountProbes(w http.ResponseWriter, r *http.Request, _ ownerapi.CreateTargetAccountProbesParams) {
	h.createTargetProbes(w, r)
}

func (h *OwnerAuthHandler) GetTargetAccountProbe(w http.ResponseWriter, r *http.Request, probeID openapi_types.UUID) {
	r.SetPathValue("probeId", probeID.String())
	h.getTargetProbe(w, r)
}

func (h *OwnerAuthHandler) ListBatches(w http.ResponseWriter, r *http.Request, params ownerapi.ListBatchesParams) {
	h.listBatches(w, r, params)
}

func (h *OwnerAuthHandler) CreateBatch(w http.ResponseWriter, r *http.Request, _ ownerapi.CreateBatchParams) {
	h.createBatch(w, r)
}

func (h *OwnerAuthHandler) GetBatch(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, params ownerapi.GetBatchParams) {
	r.SetPathValue("batchId", batchID.String())
	h.getBatch(w, r, params)
}

func (h *OwnerAuthHandler) UpdateBatch(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, params ownerapi.UpdateBatchParams) {
	r.SetPathValue("batchId", batchID.String())
	h.updateBatch(w, r, params)
}

func (h *OwnerAuthHandler) GetBatchPreview(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, params ownerapi.GetBatchPreviewParams) {
	r.SetPathValue("batchId", batchID.String())
	h.getBatchPreview(w, r, params)
}

func (h *OwnerAuthHandler) ProbeBatchDeliveries(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, _ ownerapi.ProbeBatchDeliveriesParams) {
	r.SetPathValue("batchId", batchID.String())
	h.probeBatchDeliveries(w, r)
}

func (h *OwnerAuthHandler) GetBatchJoinPreview(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID) {
	r.SetPathValue("batchId", batchID.String())
	h.getBatchJoinPreview(w, r)
}

func (h *OwnerAuthHandler) GetBatchDeliveries(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, params ownerapi.GetBatchDeliveriesParams) {
	r.SetPathValue("batchId", batchID.String())
	h.getBatchDeliveries(w, r, params)
}

func (h *OwnerAuthHandler) AuthorizeDeliveryReclaim(w http.ResponseWriter, r *http.Request, membershipID openapi_types.UUID, _ ownerapi.AuthorizeDeliveryReclaimParams) {
	r.SetPathValue("membershipId", membershipID.String())
	h.authorizeDeliveryReclaim(w, r)
}

func (h *OwnerAuthHandler) ActivateMembershipCard(w http.ResponseWriter, r *http.Request, membershipID openapi_types.UUID, _ ownerapi.ActivateMembershipCardParams) {
	r.SetPathValue("membershipId", membershipID.String())
	h.activateMembershipCard(w, r)
}

func (h *OwnerAuthHandler) RevokeDeliveryCard(w http.ResponseWriter, r *http.Request, membershipID openapi_types.UUID, _ ownerapi.RevokeDeliveryCardParams) {
	r.SetPathValue("membershipId", membershipID.String())
	h.revokeDeliveryCard(w, r)
}

func (h *OwnerAuthHandler) CreateJoinOperation(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, _ ownerapi.CreateJoinOperationParams) {
	r.SetPathValue("batchId", batchID.String())
	h.createJoinOperation(w, r)
}

func (h *OwnerAuthHandler) GetJoinOperation(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, params ownerapi.GetJoinOperationParams) {
	r.SetPathValue("batchId", batchID.String())
	h.getJoinOperation(w, r, params)
}

func (h *OwnerAuthHandler) CreateJoinReconciliation(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, _ ownerapi.CreateJoinReconciliationParams) {
	r.SetPathValue("batchId", batchID.String())
	h.createJoinReconciliation(w, r)
}

func (h *OwnerAuthHandler) ListJoinOperationsNeedingAttention(w http.ResponseWriter, r *http.Request, params ownerapi.ListJoinOperationsNeedingAttentionParams) {
	h.listJoinOperationsNeedingAttention(w, r, params)
}

func (h *OwnerAuthHandler) GetBatchRemovalPreview(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, params ownerapi.GetBatchRemovalPreviewParams) {
	r.SetPathValue("batchId", batchID.String())
	h.getBatchRemovalPreview(w, r, params)
}

func (h *OwnerAuthHandler) CreateRemovalOperation(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, _ ownerapi.CreateRemovalOperationParams) {
	r.SetPathValue("batchId", batchID.String())
	h.createRemovalOperation(w, r)
}

func (h *OwnerAuthHandler) CreateRemovalReconciliation(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, _ ownerapi.CreateRemovalReconciliationParams) {
	r.SetPathValue("batchId", batchID.String())
	h.createRemovalReconciliation(w, r)
}

func (h *OwnerAuthHandler) GetRemovalOperation(w http.ResponseWriter, r *http.Request, batchID openapi_types.UUID, params ownerapi.GetRemovalOperationParams) {
	r.SetPathValue("batchId", batchID.String())
	h.getRemovalOperation(w, r, params)
}

func (h *OwnerAuthHandler) ListRemovalOperationsNeedingAttention(w http.ResponseWriter, r *http.Request, params ownerapi.ListRemovalOperationsNeedingAttentionParams) {
	h.listRemovalOperationsNeedingAttention(w, r, params)
}
