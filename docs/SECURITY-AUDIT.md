# MUDP 安全审计报告（2026-08-14）

本报告基于对 `internal/`（auth / middleware / server / dockerx / mcp）与 `web/` 前端的全面代码审计，
是防御性的自检（自有代码库），并配套回归测试：
`internal/server/security_audit_test.go`、`internal/dockerx/shellquote_test.go`、
`web/tests/unit/chunkupload-layout.test.js`、`web/tests/e2e/security-hardening.spec.js`。

**修复状态（2026-08-14 第二轮）**：M-1/M-2/M-3、L-1/L-2/L-3/L-4/L-5 及 Windows rename 竞态
已全部修复并通过反转后的回归测试；**H-1（会话吊销）仍未修复**（需要产品决策 + DB 迁移，见下）。
测试约定：

- **正向回归测试**：验证已存在且正确、但此前无测试覆盖的防护，防止将来被无意削弱。
- **修复验证测试**：审计发现并已修复的问题，每个测试标注其闭合的审计项；一旦失败即代表
  对应加固被回退（原 KNOWN GAP 钉住测试已全部反转为正向断言）。

---

## 1. 漏洞清单

### H-1 会话不可吊销，登出仅是客户端行为（已知缺口）

- 位置：`internal/auth/auth.go:49-59`（`Signer.Clear` 只覆写浏览器 cookie）、
  `internal/server/server.go:655-658`（`logout` 不做任何服务端记录）。
- 会话是无状态 HMAC cookie（`auth.go:34-47`），body 仅 `userID:expiry`，**无 nonce**：
  同一 (uid, 秒级过期时刻) 的 token 完全相同，无法单独吊销或轮换。
- 影响：被窃取的 cookie 在登出后仍然有效最长 24h；管理员禁用账号可拦截（每次请求查库），
  但无法踢出活跃会话。
- 建议：用户表增加 `sessions_valid_after` 水位列（登出/改密时更新为当前时间，
  校验时要求 token 的签发时间 ≥ 水位），或引入服务端会话表 + 吊销列表。
- 既有测试：`TestSecuritySessionSurvivesLogout`（security_regression_test.go:407）已钉住该缺口。

### M-1 分块上传配额可绕过（已修复 ✅）

- 位置：`internal/server/chunkupload.go`
- 原问题：`handleChunkInit` 不校验 `size/chunkSize/totalChunks` 三者的算术一致性；
  `writeChunkSegment` 写入字节数与声明的 `ChunkSize` 无关（单 chunk 上限仅 HTTP body 160 MiB）；
  整文件 CRC32 仅在客户端提供 `fileCRC32` 时才校验，而官方前端
  `web/lib/chunkupload.js:91,158` 恒发 `fileCRC32: ""`，该校验在真实流量中从不生效。
  攻击路径：声明 `size=1MB` 通过配额投影 → 发送 N 个 160 MiB chunk → complete 拼接成超大文件。
- **修复（三层防御）**：
  1. init 校验 `totalChunks == ceil(size/chunkSize)`，且 `chunkSize ≤ 160 MiB`、
     `size > 0`（零字节隐式拒绝）——不一致布局 400，配额投影不再运行；
  2. `writeChunkSegment` 按 `chunkByteRange` 钉死每个 chunk 的精确字节数，
     短/超长 body 一律拒绝并删除 segment；
  3. `assembleChunks` 组装完成后比对总字节数 vs 声明 `st.Size`，不符即删除最终文件 ——
     即使前两层被绕过，配额绕过也被最终闭合。
- 官方前端无需改动（其布局本就自洽，见 `web/tests/unit/chunkupload-layout.test.js`）。
- 配套测试：`TestAuditChunkInitRejectsInconsistentLayout`、
  `TestAuditChunkSegmentEnforcesDeclaredLength`、`TestAuditAssembleChunksRejectsSizeMismatch`。

### M-2 `TotalChunks` 无上界 → CPU/内存 DoS（已修复 ✅）

- 位置：`internal/server/chunkupload.go`
- 原问题：`TotalChunks=50,000,000` 可使 complete/abort 分配 ~400MB 的 missing 列表并做
  同量级 `os.Remove`。
- **修复**：`maxUploadChunks = 100_000` 上界（init 拒绝超限）；`readChunkState` 同时校验
  读取到的状态文件，使加固前落盘的炸弹状态也无法触发无界循环。
- 配套测试：`TestAuditChunkInitCapsTotalChunks`。

### M-3 `uploadId` 泄露服务器绝对路径（已修复 ✅）

- 位置：`internal/server/chunkupload.go`
- 原问题：`encodeUploadID(root, rel) = filepath.Join(root, rel)`，init 的 JSON 响应直接
  返回解析后的绝对路径，暴露 netdisk 布局与 Docker volume 挂载点。
- **修复**：uploadId 改为 init 时生成的 `crypto/rand` 16 字节随机 hex 句柄，存于状态文件的
  `UploadID` 字段；chunk/complete/abort 与状态中的句柄比对（路径本身仍由 `cleanUserPath`
  在每次调用时重新收容，因此句柄纯为完整性检查）。升级前遗留的空句柄状态会在下次
  resume-init 时原地升级。
- 配套测试：`TestAuditChunkInitUploadIDIsOpaque`。

### L-1 `/api/logout` 不受 CSRF 保护（已修复 ✅）

- 位置：`internal/server/server.go`
- 原问题：logout 注册在三个带 `CSRFProtect` 的路由组之外，跨站页面可强制受害者登出。
- **修复**：路由移入主认证+CSRF 组。前端 `api()` 本就携带 `X-CSRF-Token` 并能在
  `/csrf/i` 错误时经 `GET /api/me` 自愈重试；UI 登出流程回归由 e2e
  `admin-console.spec.js:939`（"logout returns to the login screen"）验证。
- 配套测试：`TestAuditLogoutRequiresCSRF`（Go）、security-hardening.spec.js 第 4 项（e2e）。

### L-2 `streamTarFile` 宽松条目匹配（已修复 ✅）

- 位置：`internal/server/container_files.go`
- 原问题：`name == expected || Typeflag == TypeReg` 使任意第一个普通文件条目被当作
  目标文件流式返回，伪造归档可"下载 A 实得 B"。
- **修复**：匹配改为名字 AND TypeReg；无匹配条目时以 EOF 结束且不输出任何字节。
- 配套测试：`TestAuditStreamTarFileServesOnlyNamedEntry`。

### L-3 容器→zip 流的成员名无 `..` 检查（ZipSlip，已修复 ✅）

- 位置：`internal/server/container_files.go`
- 原问题：zip 条目名仅 `TrimPrefix("/")`，恶意容器可让下载的 zip 在用户本机解压时
  写出目标目录之外。
- **修复**：与 `extractContainerTar` 相同的 `path.Clean` + 包含检查（`..`/`../` 前缀/
  绝对路径/`.` 一律跳过该条目）。
- 配套测试：`TestAuditStreamContainerTarAsZipRejectsTraversalNames`、
  `TestAuditStreamContainerTarAsZipSkipsSymlinks`。

### L-4 CSRF token 允许经 query 传递（已修复 ✅）

- 位置：`internal/middleware/csrf.go`
- **修复**：移除 `?csrf_token=` 兜底，仅保留 header 通道。前端所有 mutating 请求均走
  header（审计确认无任何使用 query 形式的调用方）。

### L-5 `X-Forwarded-Proto` 对任意来源可信（已修复 ✅，cookie Secure 标志残留已接受）

- 位置：`internal/middleware/secheaders.go`（主监听 + `mcp_remote.go` 外部监听）
- 原问题：直连客户端伪造 `X-Forwarded-Proto: https` → 明文 HTTP 上输出 HSTS，
  钉住一个可能没有 HTTPS 的主机。
- **修复**：`SecurityHeaders(trusted)` 签名改为接收 `MUDP_TRUSTED_PROXIES` 解析结果，
  XFP 仅在请求来自可信代理时被采信（与限流侧 `ClientIP` 同一纪律）。外部 MCP 监听器
  信任其环回隧道守护进程（`loopbackTrusted`），行为不变。
- **残留（接受）**：会话/CSRF cookie 的 `Secure` 标志仍直接信任 XFP（`httpx.IsSecureRequest`）。
  伪造该头只会让浏览器丢弃自己的 cookie（自扰），无跨用户影响，不值得为它把代理配置
  穿透进 auth 层。
- 配套测试：`TestAuditXForwardedProtoIgnoredFromUntrustedPeer`（Go）、
  `TestSecurityHeadersHSTSOnSecureRequest` 新增不可信对端用例。

### L-6 登录错误消息差异造成用户名枚举

- 位置：`internal/store/store.go:1013`（"user is disabled"）vs `:1006/:1019`
  （"invalid username or password"）。时序已用 dummy bcrypt 抹平，但消息差异确认账号存在。
- 建议：统一消息（disabled 状态可延迟到登录成功后再告知）。

### L-7 分享密码经 URL query 传递

- 位置：`internal/server/netdisk.go:1436`（`?password=`）。`X-Share-Password` header 通道
  已存在（share.js 默认使用），query 兜底使密码进入代理/访问日志。
- 建议：仅保留 header + 表单 POST。

### L-8 非常数时间比较（卫生问题）

- `internal/server/server.go:1981`：Feishu state 先做普通 `!=` 比较再 `hmac.Equal`；
- `internal/server/mcp.go:205`：MCP 外部 key 的 SHA-256 摘要用 `!=` 比较（比较的是摘要，
  实际不可利用）。
- 建议：统一 `subtle.ConstantTimeCompare` / `hmac.Equal`。

### L-9 其他已确认的低危项

| 项 | 位置 | 说明 |
|---|---|---|
| `sanitizeFilename` 放行 `"` 与 `\` | netdisk.go（已有 pin 测试 security_test.go:317） | Content-Disposition 头注入气味 |
| CSV 导出公式注入 | security_test.go:484 已钉住 | 前缀 `'` 消毒待做 |
| `MUDP_SESSION_SECRET` 无最小强度校验 | config.go:50-54 | 1 字符密钥可保护全部会话签名 |
| `randomToken` 取模偏差 | netdisk.go:1779-1781 | 62 字母表 `%256` 偏差，熵轻微降低 |
| TOCTOU（resolve-then-open） | netdisk.go:197-200（已自注释） | Windows 无可移植 `O_NOFOLLOW` |
| `TRACE` 方法免 CSRF | csrf.go:88 | 当前无 TRACE 路由，面过宽 |

### 信息级（设计内行为，已由 admin 组约束）

- 磁盘挂载/卸载、备份落盘、组网盘根设置均接受任意宿主路径（`disks.go`/`backup.go`），
  已全部收在 admin-only 路由组（server.go:529-534）。
- MCP token 经 URL path 明文传输（mcp.go），按 SHA-256 查找 —— 建议 Bearer header。

### 测试期间发现的可靠性缺陷（非安全，Windows 平台，已修复 ✅）

`TestHandleChunk_ConcurrentUploadsDontLoseUpdates`（chunkupload_test.go:287）在 Windows 上
偶发失败：`os.Rename` 报 "Access is denied"，原因是并发 `readChunkState` 短暂打开状态文件时，
Windows 的 rename-onto-open-file 语义（即使 `FILE_SHARE_DELETE`）会拒绝替换。
**修复**：`writeChunkState` 改用 `renameWithRetry`（6 次递增退避重试），读者持文件仅微秒级，
重试将其转为确定成功。修复后连续多轮 `-count` 稳定通过。

---

## 2. 已验证的强防护（本次补齐测试）

以下防护此前**没有测试覆盖**，本次新增正向回归测试锁定：

| 防护 | 位置 | 新测试 |
|---|---|---|
| `extractContainerTar` 拒绝 `..`/绝对路径逃逸 + 符号链接跳过 + 负 Size 跳过 | container_files.go:341-403 | `TestAuditExtractContainerTar*` |
| `streamContainerTarAsZip` 跳过符号/硬链接 | container_files.go:124 | `TestAuditStreamContainerTarAsZipSkipsSymlinks` |
| `escapePathForPowerShell` 单引号转义阻断 PS 注入 | symlink.go:124-128 | `TestAuditEscapePathForPowerShell` |
| `CreateSymlink` 拒绝带分隔符/遍历的 displayName | symlink.go:25-27 | `TestAuditCreateSymlinkRejectsTraversalNames` |
| `shellQuote` POSIX 单引号转义阻断 in-container 注入 | dockerx/container_files.go:245-247 | `TestShellQuote*`（dockerx 包） |
| 分块上传客户端布局一致性（size/chunkSize/totalChunks 自洽，chunkRange 不越界） | web/lib/chunkupload.js | `chunkupload-layout.test.js`（vitest） |
| 分享链接：密码门禁 + 越权路径 403 + 分块上传端到端 | netdisk.go:1110-1160 | `security-hardening.spec.js`（Playwright） |

审计同时复核并确认了既有防护未被削弱：SQL 参数化、CSRF 双提交（常数时间）、
会话 HMAC 签名与过期、bcrypt + 反枚举 dummy hash、cleanUserPath 双重包含检查与
symlink 解析、exec 全部 argv 化、SSRF 元数据 IP 拒绝、CL.TE 走私拒绝、CSP、
XSS 回归（security-xss.spec.js）等 —— 详见 `security_regression_test.go` 与其引用。

---

## 3. 剩余待办与优先级

已修复：M-1/M-2/M-3（chunk 上传三层加固）、L-1/L-2/L-3/L-4/L-5、Windows rename 竞态。
剩余按优先级：

1. **H-1** 会话吊销水位列（涉及一次 DB 迁移 + cookie 格式加 `iat`，会影响既有
   `TestSecuritySessionSurvivesLogout` 钉住测试与全部在线会话；需先对齐
   "登出 = 登出所有设备"的产品语义，属产品决策，未在本轮修复）。
2. **L-6** 登录错误消息统一（消除用户名枚举）；**L-7** 分享密码仅保留 header 通道。
3. **L-8** 两处非常数时间比较统一为 `subtle.ConstantTimeCompare`/`hmac.Equal`。
4. **L-9** 表中各项（session secret 最小强度、CSV 公式消毒、`randomToken` 拒绝采样等），
   按顺手合入。
