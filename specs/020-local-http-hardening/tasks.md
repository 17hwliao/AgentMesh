# 020 任务

- [x] T001 在 API Key 创建成功响应设置 `Cache-Control: no-store`，并覆盖单次原始 Key 响应边界。
- [x] T002 以显式、可测试的 `http.Server` 替换隐式监听，固定读头/读/空闲超时并保持 SSE 写超时为零。
- [x] T003 为进程内限流桶加入可注入时钟下的 TTL 回收与容量 fail-closed，补边界测试和 README 说明。
- [x] T004 全量验证、Adaptive 校验、私有阶段复盘、提交并 fast-forward `master`；不 push/tag/Docker。
