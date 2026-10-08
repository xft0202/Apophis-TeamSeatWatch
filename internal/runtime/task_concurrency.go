package runtime

import (
	"net/http"

	"github.com/xft0202/Apophis-TeamSeatWatch/internal/generated/ownerapi"
)

func validConcurrency(value *int) bool { return value == nil || *value >= 1 && *value <= 100 }
func selectedConcurrency(value *int) int {
	if value == nil {
		return 1
	}
	return *value
}

func (h *OwnerAuthHandler) GetProxyTaskCapacity(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticated(w, r, false); !ok {
		return
	}
	var limit, healthy int
	var mode ownerapi.ProxyTaskCapacityMode
	err := h.pool.QueryRow(r.Context(), `SELECT routing_mode,task_concurrency,(SELECT count(*) FROM tsw_proxy_pool_nodes WHERE state='healthy' AND NOT retired AND (stable_until IS NULL OR stable_until>now()+interval '2 minutes')) FROM tsw_proxy_pool_settings WHERE id=true`).Scan(&mode, &limit, &healthy)
	if err != nil {
		writeProblem(w, r, 500, "proxy_pool_unavailable", "Internal Server Error", "并发设置读取失败", 0)
		return
	}
	writeJSON(w, 200, ownerapi.ProxyTaskCapacity{Mode: mode, TaskConcurrency: limit, HealthyCount: healthy})
}
