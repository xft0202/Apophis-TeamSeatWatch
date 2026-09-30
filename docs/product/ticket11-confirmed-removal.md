# Ticket 11：授权绑定的逐席清退

## 边界

清退消费 Ticket 10 的原 preview、authorization digest、明确 assignments、policy version、授权 session 与冻结 epoch_versions。它不重建授权、不按新成员列表重新配对、不扩大范围，也不改写 Ticket 10 的源事实/epoch。新入口仅位于 Mantine rebuild；正式 Owner/Public 入口仍待 Ticket 17 验收后切换。

生产 adapter 使用现有受控出口和保存的 Workspace bearer。固定协议为 `DELETE https://chatgpt.com/backend-api/accounts/{workspaceID}/users/{frozenMemberID}`、空 body、Workspace bearer、`Chatgpt-Account-Id`、device/origin/admin-members referer。协议参考只读仓库 `kxj-gpt-reg-go-dev-next/internal/thirtydayteam/members_remove.go` 与 `members_sync.go`。Member 只读核验与 DELETE 都禁用保存 Cookie 和 ambient cookie jar，只有已核验的 Workspace bearer 可提供认证身份。不使用旧 Basic Auth 网关、Personal bearer、self-leave 或按 email 临时推断删除 ID。现场兼容性仍需人工验证；开发验收没有真实平台请求。

## 可恢复状态

`tsw_rotation_removals` 固定原授权与启动 key；`tsw_rotation_removal_slots` 保存每个原成员、原账号、固定候选、租约 owner/token/递增 epoch/expiry、attempt、错误、唯一 request identity/time、verification identity 与更新时刻。`tsw_rotation_removal_evidence` 保存不可变完整 roster、当前 exchange、Owner 证据、授权 digest、核实 lease epoch 与观测时间。

| 状态 | 含义 | 是否释放给候选 |
| --- | --- | --- |
| pending | 尚未清退 | 否 |
| lease_acquired | 当前 worker 预检中 | 否 |
| remove_requested | 请求意图已提交，可能已经到达远端 | 否 |
| remote_result_uncertain | 超时/失败/仍在席，保留原请求义务 | 否 |
| absent_verification_pending | 收到回执或正在核实原请求 | 否 |
| absent_verified | 完整缺席证据与状态原子提交 | 仅在后续门禁仍有效时 |
| blocked | 未通过预检，未将回执冒充空位 | 否 |
| stopped | 停止/撤权/仅保留核实结果 | 否 |

数据库保护固定范围、请求标记与证据，禁止擦除请求或删除义务。请求标记提交后，任何重入、失联响应、不同 worker 或进程重启均只能核实该原请求，不能再次 DELETE。同一 preview 使用确定性启动 key；不同 key 冲突。

## 检查与 fencing

1. 每次动作验证原 facts/digest/assignments、原授权 session 与当前 Owner session、母号/空间/draft 绑定、当前凭据、原账号 used/ever_used 证据与全局保护、所有冻结 epochs。
2. 每次远端写入前，重新读 Workspace Owner 身份及完整成员 roster。匹配冻结 member ID、规范账号 identity、role、seat type；只能减去本授权已经提交 `absent_verified` 的原成员。重复、alias 冲突、目标变化、不完整分页均 fail closed。
3. 持久化唯一 request marker 后才 dispatch。2xx/404 不释放原槽；必须用当前 Workspace 凭据重新读取有效 Owner 身份和完整 roster，证明冻结 member ID、身份别名与规范账号均不存在，并成功原子提交证据。
4. 事务只覆盖本地检查/租约/意图/证据提交，不跨网络持有 writer-epoch transaction。独立数据库 session 的 shared action locks 防止覆盖源事实在 dispatch 间隙提交；覆盖事实写入取得对应 exclusive transaction lock。
5. 锁顺序为排序后的 source action locks → workspace gate。workspace gate 只覆盖 claim、dispatch 与提交，不覆盖只读网络预检/核实；停止或撤权能及时 fence 尚未发出的动作。已发出的请求不能被声称取消。
6. 同空间最多一个活跃清退 lease，避免两个同时消失却尚未落证的成员互相阻塞 roster 证明。原请求的失败结果与已确认成功槽独立保留，不重开整批。
7. dispatch lease/context 同时受原授权、当前 Workspace token、原授权 session 与当前请求 session 的 idle/absolute expiry 约束；只读停止后核实仅受当前合法 session 约束。所有结果提交均检查 owner/token/epoch 与数据库时间 expiry。即使尚无 takeover，过期 lease 也不能落证。停止/撤权推进 fencing 并清除 lease，不擦除已发出的义务。
8. 停止、撤权、授权过期或源 epoch 漂移之后，可以使用当前合法 Owner/空间凭据对原请求做显式只读核实；仅记录原槽观察，不重新授权、不发 DELETE、不释放给候选。

## Ticket 12 接口

只消费 `tsw_rotation_released_slots`，不能把 receipt、计数减少、验证 ID 非空、`pending`/`stopped`/不确定槽当空位。该 view 同时要求：已提交 `absent_verified`、没有不确定义务、同一未停止授权与 digest、授权未撤销/过期、原授权 session 有效、全部 frozen epochs 不变，且缺席证据仍在 30 秒新鲜窗口内。

已确认空位允许通过 `verify` 在新的 fenced lease 下重新取得证据；仍只读取原槽，绝不重复 DELETE。Ticket 12 在 claim/远端写入/结果提交时还必须重新检查授权、Owner/Workspace 权限、当前凭据、精确 assignment、占用与全部 epochs。view 是门禁，不是永久席位预留或加入/交付成功标记。

## Owner API 与界面

- `POST/GET .../previews/{previewId}/removal`：明确建立逐槽记录/读取进度。
- `POST .../removal/slots/{slotId}/run`：该原槽的明确动作；已有 marker 则只核实。
- `POST .../removal/slots/{slotId}/verify`：只读核实原请求，包括停止后的观察及缺席证据刷新。
- `POST .../removal/stop`：停止后续动作，保留已发请求。
- `GET .../expiry-rotation/removals?page=...`：Owner 范围内分页历史，无远端请求。

界面显示原成员、原槽状态、固定候选与下一步。刷新/历史恢复/轮询只读取持久化进度，不隐式创建记录或执行清退。停止按钮不被正在进行的单槽动作禁用；原席待核验时明确显示不可加入。历史任务与当前 draft 独立恢复。秘密、技术错误响应和原始凭据不会渲染为状态文案。

## 回归覆盖

平台协议测试固定 Workspace/member/body/header 与身份别名、分页失败边界。真实 disposable PostgreSQL + 官方 adapters 的 mock HTTP 覆盖：

- 无授权、撤权、过期、digest 错误、不同启动 key、非原授权 slot。
- 每一个 frozen scope epoch 漂移，包括变化后恢复源值。
- 当前 Owner 角色、member ID、role/seat type、重复身份与 alias 冲突。
- 全局保护和原 usage evidence 漂移。
- 部分成功、不完整分页、2xx/404 receipt、timeout 应用/未应用、429/5xx。
- 请求后进程重建、重复 run、数据库落证失败、lease 过期和 supersession。
- 双 worker 一次 DELETE、不同槽独立结果、事实写入不能跨 dispatch 提交。
- 预检/已发请求/核实三个边界上的停止与撤权；迟到 worker 不得释放槽。
- 停止/撤权后的只读核实、已确认缺席证据更新、历史恢复不触发远端读取。

Web 测试覆盖八种状态、receipt/restart 只能核实、实时 lease/授权漂移阻止 dispatch、确定性启动 key、只读恢复、停止文案与不会提前启用候选加入。全量 Go/Web 验收和独立审查必须完成后才能合并；这里列出的覆盖不替代实际通过的命令结果。
