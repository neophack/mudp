# MUDP 架构与模块分析

> **MUDP** = **M**ulti **U**ser **D**ocker **P**latform —— 一个单二进制的多用户 Docker 管理面板。
> 支持多用户隔离、RBAC 角色、容器生命周期、网盘、栈编排、GPU/资源监控。
> 后端纯 Go（无 CGO），前端原生 ES Module SPA，SQLite 存储，跨 Linux/Windows/macOS 编译运行。

本文是对项目各模块的诚实技术分析，包含代码组织评估与已发现的问题清单，供维护与重构参考。

---

## 1. 项目结构总览

```
mudp/
├── cmd/mudp/main.go              (55 行)   入口：加载配置、打开+迁移 DB、启动 HTTP 服务、优雅关闭
├── internal/
│   ├── auth/                               会话与第三方登录
│   │   ├── auth.go              (72 行)    HMAC 签名的会话 cookie
│   │   └── feishu.go            (148 行)   飞书/Lark OIDC OAuth 客户端
│   ├── config/config.go         (53 行)    环境变量配置；会话密钥随机生成
│   ├── dockerx/                           Docker SDK 封装层（见 §3）
│   ├── server/                            HTTP 路由与业务处理器（见 §4）
│   └── store/store.go           (1259 行)  SQLite 存储、schema、迁移、查询
├── web/                                   前端（go:embed 嵌入二进制）
│   ├── embed.go                            go:embed 入口
│   ├── index.html / app.js / styles.css    外壳与样式
│   ├── vendor/ (xterm.js 等)               本地终端库
│   └── modules/ (21 个 JS 模块)            各功能视图（见 §5）
├── docs/                                  文档
├── scripts/build.ps1                      构建脚本
├── build.bat / README.md / go.mod / go.sum
└── mudp.db (+ .db-shm / .db-wal)          运行期 SQLite 文件
```

共约 35 个 `.go` 文件（29 个非测试）。最大的几个文件：`store/store.go`(1259)、`server/server.go`(1321)、`dockerx/docker.go`(1154)、`server/netdisk.go`(698)、`server/images_ext.go`(390)。

---

## 2. 各层职责划分

### cmd/mudp
仅做启动装配：`config.Load()` → `store.Open+Migrate` → `server.New` → `app.Routes()` 在 `cfg.Addr`（默认 `0.0.0.0:9000`）上 serve；监听 SIGINT/SIGTERM 做 10s 超时的优雅关闭。**端口模型**：主端口（管理 UI + 业务 API）由 main.go 的 `http.Server` 监听；MCP/SSE 独立端口由 `server.New` 在内部启动（监听端口、来源白名单、对外域名由 DB 中的 `mcp_policy` 决定，管理员可热重载），随 `App.Close()` 一并关闭。两个 listener 共享 `App` 但路由独立——MCP 端点（`/mcp/{token}`、`/sse`、`/messages`）只注册在 SSE listener 的 `McpRoutes()` 上，主端口完全不暴露 MCP 攻击面。

### internal/auth
- `auth.go`：基于 HMAC-SHA256 签名的 cookie 会话，无服务端会话存储（无状态）。
- `feishu.go`：飞书 OAuth 登录回调，新用户落库为 `pending`，待管理员分配角色。

### internal/config
全部来自环境变量（`MUDP_ADDR`、`MUDP_DB`、`MUDP_ADMIN_USER`、`MUDP_ADMIN_PASSWORD`、`MUDP_DOCKER_HOST`、`MUDP_WEB_DIR`）。未设置 `MUDP_ADMIN_PASSWORD` 时，首次启动会进入 Web 初始化向导创建管理员；设置后则自动创建该账号。会话密钥默认每次启动随机生成——若未设 `MUDP_SESSION_SECRET`，每次重启所有会话失效；日志会输出 WARNING 提示。

### internal/store
SQLite + modernc.org/sqlite（纯 Go 驱动，无 CGO）。连接池 8 开 4 闲。10 张表：`users / groups / user_groups / images / group_images / audit_logs / stacks / settings(kv) / resource_samples / netdisk_shares`。外键级联开启。

### internal/dockerx
Docker SDK 封装。见 §3。

**容器网络隔离模型（WRT 网关）**：mudp 创建的每个容器默认挂到 internal bridge `mudp-mesh`（`Internal:true`，本身无 NAT 出口），并 `cap-drop NET_ADMIN,NET_RAW`；出站流量经特权网关容器 `mudp-wrt`（挂 `mudp-mesh` + 普通 bridge `mudp-wrt-wan`）做 NAT——网关为 ImmortalWrt 路由器，mudp 通过 `docker exec` 注入 UCI 配置：LAN 侧（mesh）`172.31.252.2/22`、WAN 侧 `172.31.248.2/22` 默认网关 `172.31.248.1`，firewall 规则 DROP 目的为 RFC1918/docker 网桥/回环的流量、WAN masquerade 出站。最终效果：容器可访问公网，但访问不了局域网、宿主机、Docker daemon。Docker 内置网络（`bridge`/`host`/`none`）在创建和事后编辑时都被拒。`mudp-mesh`（LAN 侧）在网络页以 system 只读条目出现、用户可 inspect 看到自己容器挂在哪里；`mudp-wrt-wan`（WAN 侧）仍隐藏。整个隔离模型由 **Networks → WRT 网关** 卡片的 `wrt_policy`（store kv）驱动：启用开关、镜像名、LAN/WAN 子网与网关，保存后热重载 mesh/WAN 网络与网关容器。注意 Docker 网络 IPAM 子网创建后不可变——改子网需在宿主机 `docker network rm mudp-mesh mudp-wrt-wan` 后重启 mudp。创建容器表单里 `mudp-mesh` 默认勾选，用户可取消改挂别的网络。网关容器 `mudp-wrt` 在**管理员**容器列表中以只读 system 行显示（Owner=system，显示镜像与端口映射，可 start/stop/restart/logs/inspect，但不能 remove——重建走 WRT 卡片）；普通用户看不到。网关镜像 `hkbase/immortalwrt:latest`（特权运行、headless、mudp 用 UCI 配置 LAN/WAN/firewall）缺失时 **mudp 自动拉取**（平台基础设施，区别于用户镜像从不自动拉取）；首次启动可能耗时几分钟。拉取失败（无 registry 连通性 / 私有 registry 未配凭据）时降级为"无外网但 LAN/本机仍隔离"。store key 兼容：`WRTPolicy()` 先读 `wrt_policy`，miss 回退老的 `egress_policy`，平滑迁移不丢配置。注：compose/stack 路径不经 `CreateContainer`，不受此隔离覆盖。

### internal/server
HTTP 层，chi 路由。见 §4。

---

## 3. internal/dockerx 模块分析

| 文件 | 行数 | 职责 |
|---|---|---|
| `docker.go` | 1154 | 容器生命周期（Create/List/Action/Restart/Inspect）、镜像 pull/tag/remove/list、exec attach、日志（一次性+流）、GPU 入口、TopProcesses、挂载/网络解析 |
| `stats.go` | 173 | 一次性 + 流式容器统计（CPU/mem/net/blkio/pids）+ GPU 富化 |
| `gpu.go` | 75 | nvidia-smi 可用性缓存 + GPU 快照缓存（3s TTL） |
| `image_ext.go` | 220 | 镜像详情、build、prune、import、save、tag、push |
| `registry.go` | 86 | registry 认证 blob、主机提取、登录测试 |
| `compose.go` | 190 | docker-compose 项目目录管理、up/down 运行、env 拆分/序列化/替换 |
| `system.go` | 152 | 仪表盘系统快照、ping、round2、hostname |
| `volume.go` | 152 | mudp 托管卷 CRUD + prune |
| `network.go` | 137 | mudp 托管网络 CRUD |

### dockerx 内部的问题

- **`docker.go` 是一个 1154 行的"杂物间"**：容器、镜像、exec、日志、GPU、端口、挂载、网络全在一个文件。按域拆分（`container.go`/`image_managed.go`/`exec.go`/`logs.go`/`mount.go`）会清晰得多。
- **GPU 逻辑分散在 3 个文件**：`docker.go:518-620` 定义 `GPUUsage/gpuMetric/parseGPUMetrics/selectGPUMetrics`；`gpu.go` 负责缓存却要回调 `docker.go` 的 `parseGPUMetrics`；`stats.go:88-95` 又调用 `GPUUsage`。应统一到 `gpu.go`。
- **镜像域被劈成两半**：`docker.go` 有 `PullAndTag*/RemoveManagedImage/ListManagedImages`，`image_ext.go` 有 `ListImagesDetailed/BuildImage/PruneImages/...`，没有统一的拆分原则。
- **内存计算重复实现**：`docker.go:801` 的 `memoryMB()` 手解 `memory_stats` 算 `usage-cache`；`stats.go:127` 的 `parseStats()` 用更完整的结构再做一次同样的解码与减法。两套解码器。
- **`round2` 定义在 `system.go:143`**，却被 5+ 个无关文件使用——通用数值助手放错了文件。
- **`parseMount` 在 `docker.go:1068`**，而挂载/卷的领域文件是 `volume.go`。

---

## 4. internal/server 模块分析

| 文件 | 行数 | 职责 |
|---|---|---|
| `server.go` | 1321 | 路由、中间件、login/me/logout、**用户 CRUD、组、镜像目录、脚本、pull、建容器、容器动作、终端/WebSocket、密码**、飞书 OAuth、JSON 辅助、完整 WebSocket 帧实现、`pumpTerminal` |
| `images_ext.go` | 390 | imagesDetailed、imageBuild/Import/Save/Prune/Tag/Push、**容器 stats 流、容器 logs 流**、sseSender、registryAuthForRef、sanitizeFilename |
| `netdisk.go` | 698 | 全部网盘 + 分享处理器 + zip/copy + 内联 HTML 页 |
| `stacks.go` | 312 | 栈 list/create/get/delete/up/down + SSE 运行器 |
| `disks.go` | 170 | 磁盘列表（PowerShell/mount）、mount/unmount、backup、groupNetdisk |
| `registries.go` | 160 | registry CRUD + 测试 |
| `users_ext.go` | 123 | userUpdate、userDelete、`decodeJSON`、**`record`（审计辅助）** |
| `dashboard.go` | 106 | 仪表盘聚合 + 使用量汇总 |
| `volumes.go` | 99 | 卷 list/create/delete/prune |
| `authz.go` | 89 | 角色排名、requireRole、canMutate、recoverPanic |
| `networks.go` | 78 | 网络 list/create/delete |
| `resources.go` | 55 | 资源采样 + 历史 + admin processes |
| `audit.go` | 26 | 审计列表端点 |

### server 内部的关键错位问题

1. **`images_ext.go` 是最名不副实的文件**：尽管叫"images_ext"，它装着**容器 stats 流**（`containerStatsStream`）和**容器 logs 流**（`containerLogsStream`）——这两个是容器端点，只因共用 SSE 辅助被塞进了镜像文件。排查容器日志流问题的人不会去镜像文件里找。
2. **审计写辅助 `record()` 在 `users_ext.go:14`**，而审计读处理器在 `audit.go`。写逻辑应与审计域同放。
3. **`sseSender` 定义了却被忽略**：`images_ext.go:267` 抽出了可复用的 `sseSender()`，但 `server.go` 在 466/686 行、`stacks.go` 在 263 行各内联了一份相同的闭包。
4. **`server.go` 是 1321 行的巨石**：用户、组、镜像、pull、建容器、终端、WebSocket 协议、飞书 OAuth、JSON 辅助全在一起。`_ext.go` 约定本意是分摊，但只用在 2 个文件上且语义不一致。
5. **`_ext.go` 后缀无统一含义**：`server/images_ext.go`（镜像操作+容器流）、`server/users_ext.go`（用户改删+解码+审计）、`dockerx/image_ext.go`（镜像 build/import/save/tag/push）。`pullImage` 在 `server.go`，`imagePush` 在 `images_ext.go`——同域处理器分散在两文件。

路由注册本身集中在 `server.go:51-164` 的 `Routes()`，是好的；问题只在处理器实现的散乱。

---

## 5. web/modules 前端分析

21 个 JS 模块，均为原生 ES Module，无框架、无构建步骤。`app.js`(294) 持全局状态、核心 helper、tab 路由；各 `renderX` 渲染进 `#view`。

### 前端的主要问题

- **`escapeHtml` 在 16 个文件各拷贝一份**（app.js 导出一份，另有 15 个模块各自定义同名同实现的 8 行函数）：`audit/create/details/images/logs/networks/pending/settings/stacks/stats/terminal/ui/usage/users/volumes` 与 `app.js`。维护隐患：改一处要同步 16 处。
- **`fmtBytes` 定义两次**（`disks.js:56`、`netdisk.js:170`），**`fmtMB` 两次**（`dashboard.js:197`、`volumes.js:100`），均未导出。
- **循环依赖**：`app.js` ↔ 所有功能模块（双向）；`ui.js` ↔ `terminal.js`（`ui` 导入 `closeTerminal`，`terminal` 导入 `closeModal`）。靠 ES Module 惰性解析侥幸可用，但阻碍重构。

---

## 6. 已发现问题清单（按严重度排序）

### 严重（数据/正确性）

1. **`widenRoleConstraint` 重建会丢 `port_prefix` 列**（`store.go` 重建 DDL 与 `Migrate` 建表 DDL 不一致）。任何带旧角色 CHECK 约束的库走此迁移路径时，重建 users 表会丢列，随后的 `alter table add column port_prefix` 又以 `default 0` 重建——**静默清空所有用户的端口前缀**。这是真实数据丢失 bug。
2. **迁移错误被静默吞掉**（`store.go:209-218`）：每个 `db.Exec("alter table ... add column ...")` 都 `_` 丢弃返回错误，真实 DB 错误与"列已存在"无法区分；无迁移版本表。
3. **`MarshalEnv` 注释承诺"确定性排序"但未排序**（`compose.go:183-190` 及 `.env` 写入 `53-62`）：遍历 map 输出顺序随机，与文档承诺矛盾。生成的 `.env` 与序列化 JSON 非确定。
4. **注册表 JSON blob 读-改-写竞态**（`server/registries.go:38-77` + `store.go:1247`）：整个列表是 `settings` 表的一行 JSON，两管理员同时编辑会丢一次更新，无行级锁。
5. **`CreateFeishuUser` check-then-insert 竞态**（`store.go:710-720`）：`count(*)` 后无事务地插入，唯一索引兜底但向用户返回原始错误而非重试。

### 中等（可维护性/安全）

6. **`escapeHtml` 复制 16 份**（见 §5），维护与一致性风险。
7. **`server.go`(1321)/`docker.go`(1154) 过大**，`_ext.go` 约定无意义（见 §3、§4）。
8. **N+1 查询**：`Users()`(`store.go:368`)、`ImagesForUser()`(`449`)、`StacksForUser()`(`1195`)、`AllNetdiskShares()`(`1057`) 列表为每行发一条 group/owner 名查询；仪表盘 `buildUsage()` 对每用户循环 `ListContainers`，冷缓存时还会各起一次 `nvidia-smi`。
9. **管理员口令未设置时随机生成**（`config.go`）；会话密钥默认每次启动随机（`config.go`），未设 `MUDP_SESSION_SECRET` 时每次重启登出全部用户，但会输出 WARNING 日志。
10. **令牌明文存储**（`store.go:1218` 注释自承认）：registry token、飞书 app secret 以明文存于 `settings` 表。
11. **磁盘 mount/unmount 把用户路径拼进 PowerShell/`mount`**（`disks.go:78,84,104,106`）：PowerShell 用 `%q` 不是稳健的 shell 转义；仅管理员可用降低了风险，但仍是命令注入面。

### 轻微（整洁/死码）

12. **疑似死码**：`server/authz.go:59` 的 `admin()` 包装未被引用（路由直接用 `minRoleMiddleware(rankAdmin)`）；`dockerx/docker.go:1061` 的 `VolumeMount` 导出但无调用；`store.DeleteNetdiskShare` 单 token 版未用（调用方都用多 token 版）。
13. **内联大段 HTML**：`netdisk.go:407` 把约 3000 字符的分享页 HTML/JS 作为 Go 字符串字面量，`netdisk.go:647` 又一份"不可用"页——应放进 `web/` 嵌入 FS。
14. **零 `TODO/FIXME/HACK` 注释**（全仓 grep 无命中），唯一接近的是 `store.go:1219` 的散文式"follow-up"。优点是无遗留标记；缺点是上述已知问题未在代码内标注。

---

## 7. 总体评价

项目是一个**可用、自洽的单二进制 Docker 面板**，具备合理的安全原语：HMAC 会话、bcrypt 口令、RBAC 角色排名、容器所有权守卫（`containerOwnedBy`）、网盘路径穿越防护（`cleanUserPath`）。路由集中、前端无框架依赖、纯 Go 存储驱动保证了部署与交叉编译的简洁。

主要问题是**组织性而非算法性**的：

- `_ext.go` 约定应用不一致，导致容器端点住进镜像文件、审计写辅助住进用户文件；
- `server.go` 与 `docker.go` 是过度膨胀的杂物间；
- 前端层把 `escapeHtml` 复制 16 次；
- 迁移系统静默吞错，`widenRoleConstraint` 重建可能摧毁 `port_prefix` 列。

最紧迫的具体 bug 是：`port_prefix` 数据丢失（`store.go` 重建路径）、注册表 JSON blob 竞态、`MarshalEnv` 与文档矛盾的"确定性"。这些在后续功能迭代中应优先修复。
