# 015 计划：HTTP 契约事实合并

## 契约结构

维护已有 `specs/003-local-api-key-gate/contract.md`，不在 015 新建 `contract.md`。重写后的文档按四部分组织：全局响应安全规则；端点矩阵；chat 处理顺序；admin 处理顺序。端点矩阵覆盖 `GET /healthz`、`POST /v1/chat/completions`、`GET /health/providers`、`GET /v1/observability/traces/{trace_id}` 和持久 auth mode 才注册的三个 `/admin/*` 端点。

生产 chat 路由如实记录：tenant API Key 鉴权 → 对 POST 的进程内 RateGate → handler 方法检查 → strict JSON decode → trace session/header → tenant/model route → 可注入 QuotaGate → 可选 Reservation Begin/持久 started attempt → Provider stream。每一步的拒绝码、是否读 body、是否触发后续能力都在表中标明。`rate_limited` 必须附 `Retry-After`；trace header 在 model route 判定前设置，最终流失败不反向撤销该 header。

admin 顺序独立于 tenant Key：路由仅在持久 auth mode 注册 → admin Bearer token 常量时间比较 → 对 POST strict JSON decode/route validation 或 DELETE key ID validation → lifecycle。失败 token 不读 body，tenant API Key 不能代替 admin token。trace 继续以 404 收敛跨 tenant、unknown 与 pending 情况。

## 测试与验证

- T001：以当前实现重写契约正文；只编辑已有 003 契约文件，无 Go 代码行为修改。
- T002：新增最小 HTTP 事实测试，锁定 auth-before-body、rate 429/Retry-After 的零下游、trace header/404 收敛及 admin token-before-body；测试名称与契约表对应。
- T003：README 链接到合并契约，核对现有 `make demo-stage-1`/`make demo-stage-4` 未受文档批影响；不新增或运行外部依赖演示。
- T004：全量 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check`、Adaptive；私有复盘、提交和 fast-forward `master`，不 push/tag/Docker。

章程：暂未建立；本计划不改变对外行为。
