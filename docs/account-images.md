# 用户管理与私有图片 API

2026-10-19 实现。本项目的用户身份是**租户 + 应用 API Key**：适合管理员给业务用户或应用开通网关，不是另建一套邮箱/密码登录网站。管理员使用独立 `AGENTMESH_ADMIN_TOKEN`，应用 Key 不能访问 `/admin/`。这些接口要求 `AGENTMESH_AUTH_STORE=mysql`；内存演示模式不提供持久管理/图片服务。金额、充值、配额仍沿用既有账务接口。

## 管理接口

| 方法和路径 | 用途 | 请求/返回要点 |
|---|---|---|
| POST `/admin/tenants` | 创建用户/租户 | `tenant_id`、`model_routes`；同名配置冲突返回 409，创建后的资源开通失败可用同一定义重试 |
| GET `/admin/tenants?after=...&limit=50` | 分页列用户 | `data`、`next_after`；limit 1–100，after 为上页游标 |
| GET `/admin/tenants/{tenant_id}` | 详情 | 状态、创建/修改时间、模型路由 |
| PATCH `/admin/tenants/{tenant_id}` | 禁用/启用、替换路由 | `enabled` 和/或 `model_routes`，至少一项；路由若给出则完整替换，不允许空集合 |
| POST `/admin/api-keys` | 签发新应用 Key | `tenant_id`；仅此次响应返回 `api_key`，保存 hash/prefix |
| GET `/admin/tenants/{tenant_id}/api-keys?after=...&limit=50` | 分页列 Key 元数据 | ID、前缀、状态、时间；不返回 Key 或 hash |
| DELETE `/admin/api-keys/{key_id}` | 撤销 | 已撤销重复调用仍 204；不存在返回 404 |
| GET `/v1/account` | 用户查看当前身份 | 使用应用 Key，只返回已认证租户；查询参数不能切换其他用户 |

管理响应设 `Cache-Control: no-store`，操作超时 5 秒，列表有界。状态和路由更新在同一事务中完成；签发 Key 与禁用账户通过租户行锁串行化。认证每次读取持久状态，无本地撤销缓存；禁用后**新的请求**立即拒绝，已开始的生成继续按取消/计费契约收尾。重新启用不会恢复已撤销的 Key。轮换方式是先签发、更新调用方、再撤销旧 Key。为保留账务和审计关联，本轮不硬删除租户。

创建时开通资源仍是身份事务后幂等完成：首次可能返回 503 `quota_provision_failed`，用同一定义重试；不得把不同路由的同名请求当作更新。余额充值和 Redis 配额赠额分开，签发 Key 本身不自动赠送人民币或代付模型费用。注册登录、邮件验证、用户自助邀请、每 Key 独立额度/到期/IP 策略不在此次接口契约中。

## 图片上传与存储

| 方法和路径 | 用途 |
|---|---|
| POST `/v1/images` | 直接发送图片二进制，Content-Type 为 `image/png` 或 `image/jpeg`，不是 multipart/JSON |
| GET `/v1/images/{id}` | 持同租户应用 Key 下载二进制，禁止共享缓存 |
| DELETE `/v1/images/{id}` | 删除自己的图片，成功 204；不存在或其他租户统一 404 |

上传返回 `id`、`media_type`、`bytes`、`sha256`、`created_at`、`expires_at` 和 `reference`。图片不返回公共 URL，不由供应商请求私有下载地址。未知/跨租户引用不能通过猜 ID 读取。

```json
{
  "model": "domestic-chat",
  "messages": [{"role": "user", "content": [
    {"type": "text", "text": "描述图片内容"},
    {"type": "image_url", "image_url": {"url": "agentmesh://images/img_REPLACE_WITH_RETURNED_ID"}}
  ]}],
  "stream": true,
  "max_tokens": 256
}
```

URL 须原样使用上传响应的 `reference`；上例 ID 仅示意。网关按当前租户读取，并在预算/能力检查前转换为 `data:image/...;base64,...`，上游只收到图片内容。Anthropic Messages 可在 image 的 `source.type=url` 中使用同一 reference。已有标准 HTTPS/data URL 图片方式继续支持；服务端不会从用户 URL 下载文件。OpenCode/Claude Code 不会自动使用本项目扩展的上传 API，其原有图片消息仍可按标准格式提交。

单图最多 **256 KiB、2048×2048**，只允许能完整解码的 PNG/JPEG；每租户最多 **100 张且合计 16 MiB**，7 天有效。共用 8 个图片请求并发槽和 5 秒操作超时；配额检查/写入在租户行锁下原子完成，多个实例不能并发越限。过期数据拒绝读取；每小时清理至多 100 条。未清理字节继续占用容量，防止清理停止时绕过存储上限；超限可主动删除释放。大量租户可按容量与过期积压另设运维清理任务，不承诺对象存储规模。

小图元数据和 BLOB 同事务落在 MySQL `private_images` 表；进程重启后仍可读，数据库整库备份包含图片。没有本地文件路径、公开静态目录、签名下载链接或单独 MinIO/S3 依赖。此实现针对当前 Linux 单机 Compose；后续对象存储扩展应保留租户授权、容量、过期与删除语义。

## 升级和验证

先备份数据库，在对应部署目录用**当前源码构建迁移器**，应用新增 `009_private_images.sql`，再重建/启动 Gateway。不要修改已执行迁移的历史内容。缺表时 readiness 返回 `image_storage_unavailable`，不能忽略 readiness 直接上线。无需新 volume；已有 MySQL 备份/恢复流程覆盖图片。

```sh
docker compose build am-migrate gateway
docker compose run --rm am-migrate
docker compose up -d --no-deps gateway
```

沿用自己部署原有 project、Compose 文件组合、私有 secrets 和网络，示例不包含真实 Key。自动测试 `TestRealMySQLAccountLifecycleAndPrivateImages` 使用临时 MySQL，覆盖真实 HTTP 鉴权、管理、图片完整性、跨租户、重启可读、删除、过期及 8 个并发上传竞争最后一个名额；不调用付费模型。客户端既有真实千问证据见 [接入说明](client-integration.md)。
