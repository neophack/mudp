# MUDP 优化计划

> 基于 2026-08 全量架构评审（持久层 / HTTP+Docker 层 / 安全专项 / 前端+测试+运维 四个方向）。
> 所有问题均带 `文件:行号` 证据；行号以评审时 master 为准，动手前先重新定位。
> 优先级定义：**P0** = 安全漏洞或数据正确性，立即修；**P1** = 本月内；**P2** = 本季度规划。

---

## P0 — 立即修（安全 / 数据正确性）

### P0-1 封堵卷创建 bind 宿主机任意路径（Critical）

- **问题**：`internal/dockerx/volume.go:96-101` 对 `Driver`/`DriverOpts` 零校验，`internal/server/volumes.go:32-63` 仅要求 `canMutate`。任意 activated 用户可 `POST /api/volumes {"driver":"local","driverOpts":{"type":"none","o":"bind","device":"/"}}`，再经卷文件浏览器读写宿主机文件系统。compose 路径（`compose.go:300-327`）已封禁同一手法，直建卷路径漏了。
- **叠加问题**：`volume.go:88-95` 与 `network.go:258-260` 允许用户 labels 覆盖 `mudp.*` 归属标签（容器创建 `docker.go:519-526` 已有防护），可伪造归属、创建面板不可见的"幽灵卷"。
- **修法**：
  1. `CreateVolume` 对非 admin 强制 `driver=local` 且拒绝一切 `DriverOpts`（至少拒绝 `type`/`o`/`device`）；admin 路径保留但审计记录。
  2. `CreateVolume`/`CreateNetwork` 中 `mudp.*` label 赋值移到用户 label 合并**之后**（与 `CreateContainer` 同策略）。
- **验证**：新增测试——非 admin 带 `driverOpts` 创建返回 4xx；label 覆盖不生效；admin 正常创建。

### P0-2 备份改用一致性快照

- **问题**：`internal/server/disks.go:264-276` `backupData` 直接 `io.Copy` 活库主文件，WAL 模式下最近提交在 `-wal` 中未拷贝，产物可能撕裂/缺数据。
- **修法**：先 `VACUUM INTO '<tmp>'` 生成一致性快照再打包 zip；产物显式 `0600`；`docs/operations.md` 补恢复步骤。
- **验证**：测试——写入数据后立即备份，解压恢复库校验最近记录存在。

### P0-3 补齐授权缺口（5 处）

| 端点 | 位置 | 问题 | 修法 |
|---|---|---|---|
| `containerTerminal` | `server/server.go:1841-1902` | 缺 `canMutate`，readonly 可开 root shell | 加 `canMutate` 检查 |
| `createStream` | `server/server.go:1207-1276` | 缺 `canMutate`，readonly 可建容器 | 加 `canMutate` 检查（对齐 `server.go:943`） |
| `backupJobsList`/`backupJobCancel` | `server/backup.go:589-595, 1002-1015` | 无归属隔离，可看/取消他人任务 | 按 `OwnerID` 过滤（admin 例外） |
| `imagePresetResolve` | `server/images_ext.go:593-626` | 无镜像可见性校验，preset 静态值泄露 | 加组可见性校验 |
| `/api/user/language` | `server/server.go:401-402` | 错挂 admin 组，普通用户切语言 403 | 移入已登录用户组 |

- **验证**：每处补越权测试（readonly/user 调用应 403；用户 A 操作用户 B 资源应 403；普通用户切语言 200）。

### P0-4 修复存储型 XSS（markdown 渲染）

- **问题**：`web/modules/netdisk.js:1490` 与 `web/share.js:687` 均 `innerHTML = marked.parse(text)`，marked v9 透传原始 HTML，`<img onerror>` 可执行；`netdisk.js:1487` 注释的理由是错的。当前仅靠 CSP 兜底，挡不住 CSS 注入与表单钓鱼；share.js 走公开分享页，暴露面更大。
- **修法**：vendor 一份 DOMPurify，`marked.parse` 输出经 `DOMPurify.sanitize` 后再赋值（两处统一封装进 viewer 工具）；修正错误注释。
- **验证**：vitest 用例——含 `<img onerror>` / `<form>` 的 markdown 渲染后被消毒；e2e 分享页回归。

---

## P1 — 本月内（安全加固 / 正确性）

### P1-1 迁移重建路径保留约束与索引

- **问题**：`internal/store/store.go:590-614` `widenRoleConstraint` 用 `PRAGMA table_info` 重建 users 表，丢失 `username` UNIQUE 约束与 `idx_users_feishu_open_id`（可静默产生重复用户名）；CHECK 检测用精确子串匹配，旧库 DDL 空白差异会漏检。
- **修法**：重建后显式重建 users 全部索引与唯一约束；CHECK 检测改为忽略空白/大小写的宽松匹配。
- **验证**：回归测试——重建后 username 唯一性生效、feishu 索引存在（现有测试只验列保留，需扩展）。

### P1-2 会话可撤销

- **问题**：`internal/auth/auth.go:34-47` 无状态 HMAC cookie 24h，泄露后无法吊销。
- **修法**：users 表加 `session_version` 列（新迁移），改密/禁用/降权时递增；cookie 携带版本号，校验时回读比对。
- **验证**：测试——改密后旧 cookie 失效。

### P1-3 秘密静态加密与回显抹除

- **问题**：registry token、飞书 `app_secret`、MCP token 明文、网盘分享口令明文存库（`store.go:2055-2062, 1446-1468, 509-522, 1815-1827`），备份 zip 一次带走全部；`feishuSettings` GET（`server.go:1632-1635`）完整回显 app secret（registries 列表已正确抹除，此处不一致）。
- **修法**：用 `MUDP_SESSION_SECRET` PBKDF2/scrypt 派生密钥，AES-GCM 加密上述字段（含迁移现有明文）；`feishuSettings` GET 像 registries 一样抹除 secret（写时区分"未修改占位符"）。
- **验证**：测试——库中无明文；GET 不回显；迁移后旧数据可解密。

### P1-4 WebSocket 终端健壮性

- **问题**：`server/server.go:2227-2253` 两 goroutine 并发写 conn 无互斥，字节可交错产出损坏帧；`readWSFrame` 注释声称处理分片但完全忽略 FIN 位（`server.go:2159-2160`）。
- **修法**：写路径加互斥（或单写 goroutine + channel）；`readWSFrame` 拒绝非 FIN 数据帧并修正注释（或实现分片重组）。
- **验证**：并发读写压测无帧损坏；分片帧返回明确错误。

### P1-5 代理头信任与传输安全

- **问题**：`internal/httpx/request.go:20-25`、`internal/middleware/secheaders.go:108-110` 对任意对端的 `X-Forwarded-Proto` 都信任（与 `ratelimit.go` 的 TrustedProxies 右链遍历不一致）。
- **修法**：仅当 socket 对端属于 `TrustedProxies` 时才信 XFP；启动日志在未配 TLS/反代时输出明文 HTTP 警告。

### P1-6 消除 N+1 查询

- **问题**：`Users()`（`store.go:926`）、`ImagesForUser()`（`1106`）、`StacksForUser()`（`2023`）、`AllNetdiskShares()`（`1859`）每行一次附属查询；审计列表每次请求额外调一次 `Users()`（`server/audit.go:29`）。
- **修法**：把 `NetworkGroupsByNetwork`（`dockerx/network.go:85-101`）的单查询聚合模式推广到上述四处；audit 处理器改为一次 map 或 join。
- **验证**：基准测试或查询计数断言（如 `Users()` 恒定 2 条 SQL）。

### P1-7 登录与 SSO 限流补强

- **问题**：登录仅按 IP 5 req/s（`ratelimit.go:243-245`），无账号维度节流；`/api/feishu/*` 三个端点未挂限流器（`server.go:204-206`）。
- **修法**：登录加按用户名维度节流（失败计数 + 指数退避）；`/api/feishu/*` 挂限流。

### P1-8 其余小缺口

- `CreateFeishuUser` 把 `isPortPrefixConflict` 纳入重试（`store.go:1311`，对齐 `CreateUser` 的 `store.go:787-796`）。
- 时间列统一存 UTC 或所有 prune 比较统一过 `datetime()` 归一化（`store.go:1499,1505`、`accesslog.go:414`、`mcp_logs.go:193,437`，对齐 `accessTrend` 的 `accesslog.go:388-396`）。
- `UpdateUser` role 写入加 `ValidRole` 纵深校验（`store.go:1714-1717`）。
- 审计 MCP args 预览脱敏或截断到工具名+目标（`server/mcp.go:198-203`）。
- 访问日志 CSV 导出对 `=+-@` 开头字段加 `'` 前缀中和公式注入（`security.go:716-746`）。
- chunkupload 的 uploadId 不再暴露服务器绝对路径（`chunkupload.go:99-101`，改随机 token + 服务端映射）。
- `runStackSSE` 加 `sseKeepalive`（`stacks.go:299-305`，compose up 拉镜像静默期最长）。
- disks.go 外部命令改 `CommandContext` 加超时（`disks.go:76, 190, 223`）；Docker client 调用点统一 ctx 超时常量。
- `NewComposeProject` 的 .env 写入复用已修复的 `MarshalEnv`（`compose.go:63-67` → `518-529`）。
- `POST /api/logout` 纳入 CSRF 保护组（`server.go:200`）。
- 转发端口登录门回读用户禁用/pending 状态（`forward_auth.go:100`）。

---

## P2 — 本季度（结构债 / 工程化）

### P2-1 依赖升级与 CI 安全扫描

- `modernc.org/sqlite` v1.19.1（2022-12）→ 当前 v1.29+；`golang.org/x/crypto` v0.17.0 → 最新；`x/sys`、`x/time` 顺带升级；go directive 1.21 → 与 CI 对齐（1.23）。
- `docker/docker` v25（EOL）→ v27/v28，单列一次迁移（有 API 迁移成本，otel 传递依赖随它收敛）。
- CI 加 `govulncheck ./...`、`npm audit --audit-level=high`；可选 gosec。

### P2-2 前端去重与解耦

- 删 18 份本地 `escapeHtml`（权威版 `web/lib/common.js:26`，现共 19 份）、4 份 `fmtBytes`、3 份 `fmtMB`、2 份 `countryCodeFlag`，全部直引 common；撤掉 `app.js:34` 的 re-export 桥。
- 解开 `ui.js ↔ terminal.js` 循环依赖（用回调注册，`onModalClose` 已有先例）。
- 合并 `share.js` 与 `netdisk.js` 的平行 viewer：抽 `lib/viewer.js`（markdown/pdf/图片/文本）+ 统一 api 封装——此后 viewer 修复只需一处。
- 修 `logs.js:64` 日志过滤框每击键重建整个 modal 丢焦点的问题。

### P2-3 后端组织债（一次一个域，不大爆炸重写）

- 容器 stats/logs 流迁出 `images_ext.go`（`:426, :479`）→ `container_streams.go`；审计写辅助 `record()` 迁 `audit.go`（自 `users_ext.go:15`）；统一三处内联 SSE 闭包到 `sseSender`。
- GPU 代码归拢 `gpu.go`（自 `docker.go:700-855`、`stats.go:99`）。
- 删死码：`httpx` 的 `Handler/Respond/OK/NoContent` 死抽象、`middleware.RecoverPanic`、`dockerx.VolumeMount`、`sqliteDuplicateIndex` 死常量（`store.go:193`）。
- 巨石熔断：CI 加单文件行数上限检查（如 `server.go` ≤ 2500 行），新功能强制开新文件。

### P2-4 可观测性与发布

- 日志迁移到 `log/slog`（JSON handler + 级别开关），替换 stdlib `log.Printf` 与 `httpx` 自造 SLogger。
- 加 `/healthz`（Docker daemon + DB 探活）供 systemd/反代健康检查。
- CI 加 release workflow：三平台构建 + 制品 + 校验和（复用 build.sh 的 git-sha 版本注入）。

### P2-5 文档同步

- 更新 `docs/ARCHITECTURE.md` 的模块清单、行数与问题清单至现状（或标注为历史快照 + 链接本文件）。
- 本文件每项完成后勾掉并注明完成 commit。

---

## 执行原则

1. **模式推广优先**：本项目最大特点是同一问题 A 处修好 B 处还开着（compose 防 bind vs 卷端点、TrustedProxies vs XFP、单查询 vs N+1、`sseSender` vs 内联）。每修一处先问"这个手法别处还有吗"。
2. **每个修复配回归测试**：本项目已有此传统（`TestMigrationPreservesPortPrefix`），P0/P1 项的"验证"栏为最低要求。
3. **一次一个域**：P2 重组不摊大饼，每个 PR 只动一个域，保持 diff 可审。
4. **机制大于自觉**：文件行数熔断、CI 安全扫描、覆盖率阈值（vitest 已配 v8 但无阈值）用 CI 强制，不靠口头约定。

## 进度追踪

- [x] P0-1 卷 bind 封堵 + label 防覆盖
- [x] P0-2 备份一致性快照
- [x] P0-3 授权缺口 5 处
- [x] P0-4 markdown XSS
- [ ] P1-1 迁移约束/索引保留
- [ ] P1-2 会话可撤销
- [ ] P1-3 秘密静态加密
- [ ] P1-4 WS 终端
- [ ] P1-5 XFP 可信代理
- [ ] P1-6 N+1
- [ ] P1-7 限流补强
- [ ] P1-8 小缺口 12 项
- [ ] P2-1 依赖与 CI 扫描
- [ ] P2-2 前端去重
- [ ] P2-3 后端组织债
- [ ] P2-4 slog + healthz + release
- [ ] P2-5 文档同步
