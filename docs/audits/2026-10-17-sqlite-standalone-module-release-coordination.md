# SQLite 独立 Go 模块与 Core 协调版本 — 发行工程收口

- **日期：** 2026-10-17
- **仓库 / 分支：** `orz-i/Gaoge-Agent-Runtime` / `governance/kernel-runtime`
- **原始 HEAD：** `2b1abd5`（Stage 6 已封板）
- **版本协调提交：** `56138c6`；**消费者/发行门禁提交：** `8f07099`
- **规范：** [ADR 0008（经 Accepted ADR 0009 修订）](../adr/0008-minimal-agent-runtime-architecture.md)、[ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)、[Stage 5 单项 SQLite 审计](2026-10-17-stage5-final-closure-after-schema-p2.md)、[Stage 6 性能优化审计](2026-10-17-stage6-sqlite-snapshot-buffer-optimization.md)。
- **发布说明草案：** [beta.23 候选发布说明](../releases/v0.1.0-beta.23.md)

## 裁决

**SQLite 独立模块的源码与版本化消费者兼容：本地候选 PASS。** 原始失败的根本原因不是 Kernel Store SQLite Conformance，而是模块 `go.mod` 引用已发布 `Core v0.1.0-beta.22`，其导出接口中没有 `kernel.ControlStore`、`ExecutionStore` 和后续 Stage 2 接口。不能通过向 SQLite 发行 `go.mod` 写入 `replace ../agent-runtime` 伪造解决。

本次将候选组合协调为 **`v0.1.0-beta.23` 的十个 Go 模块及同版 TypeScript 客户端**，新增 SQLite 发行边界和独立消费者验证。使用本地构造的**版本化 Go 模块代理**，在真正 `GOWORK=off`、消费者 `go.mod` **没有任何 `replace`** 的条件下，十个模块均编译、执行各自消费者用例；SQLite 原有整个模块测试也在 `GOFLAGS=-mod=readonly` / Race 下通过。

**正式公开发行仍 NO-GO，直到：** 治理 PR 通过 GitHub 必需检查并合并；root + 10 Go 模块不可变标签指向同一接受提交；发布工作流从**真实远程版本**执行 `--published` 与 SQLite 只读模块测试并 PASS；完整安全扫描与所选发布引擎门禁 PASS。此轮没有 Push、PR、Merge、Git 标签或 GitHub Release，不宣称新版本已公开。

## 修改及防回归

1. `VERSION`、`agent-runtime-boundary.json`、Go 子模块及 integration 的依赖版本、`go.work` 工作区替换、TS 包版本与根元数据均对齐 beta.23。**这是新的 SDK 候选版本，不是 SQLite 持久化 Schema 的版本更新；`schemaVersion=1` 不变。**
2. `scripts/check-beta.mjs` 将 SQLite 纳入第十个 Go 模块，并验证各模块引用的所有同仓 Go Module 版本都是 `v0.1.0-beta.23`；禁止仓库内打算发布的 Go `go.mod` 包含本地 `replace`。
3. `scripts/release-tags.mjs` 输出一个 root 标签和十个模块标签，不能修改已有 beta.22 标签；`scripts/check-release.mjs` 由 Boundary 元数据自动获得十模块列表，新增 `--published` 模式，真正**不产生任何 `replace`**，所有测试强制 `GOWORK=off`。
4. `.github/workflows/release.yml` 在完整 Beta 门禁后、产出 GitHub Release 前增加 **真正公开模块** `--published` 清洁消费者与 `cd go/agent-runtime-sqlite && GOWORK=off GOFLAGS=-mod=readonly go test ./... -count=1` 两项必经测试。**这两项必须在不可变 tags 已推送后执行**，本地模拟不替代它。
5. 新增 `contracts/consumers/go/agent-runtime-sqlite_test.go`：严格接口兼容（Store、ExecutionStore、ControlStore、runrelation.Store）及真实 SQLite Run/关系持久化、重开、幂等与缺失 Child 的行为。新 `scripts/sqlite-release-policy.test.mjs` 锁定 Module 清单/版本/标记、真实无替换门禁与步骤顺序，并纳入 `make metadata`。
6. `scripts/go-workspace.mjs tidy-check` 为未发布的同版本 Core 使用临时源替换，只忽略 tidy 因临时替换而去掉的**当前版本对应模块校验和**；其他任何 `go.mod/go.sum` 变化继续 FAIL。SQLite 的 `go.sum` 现在固定了 beta.23 Core 的真实模块内容哈希，供日后 `GOWORK=off -mod=readonly` 使用。

## 本地测试证据

| 门禁 | 结果及限制 |
| --- | --- |
| 最初的 `GOWORK=off GOFLAGS=-mod=readonly go test ./...`（旧 Core beta.22） | **FAIL（已复现）**：`undefined: kernel.ControlStore`、`ExecutionStore` 等 |
| 新 `scripts/check-beta.mjs` 与 release policy 3 项 | **PASS**，十模块元数据一致、版本一致且无发行 `replace` |
| `node scripts/check-release.mjs` | **PASS**，十模块 `GOWORK=off` 临时源替换消费者及 TS packed consumer |
| 无替换版本化代理模拟下的 `node scripts/check-release.mjs --published` | **PASS**，十模块依赖通过可下载结构的本地 ZIP 文件解析，没有使用 `go.work` 或 `replace`；**不是 GitHub 已发布证据** |
| SQLite 直接 `GOWORK=off GOFLAGS=-mod=readonly go test -race -count=1 ./...` | **PASS**，使用未公开 Core beta.23 的本地版本化 ZIP 代理和原有第三方缓存；**不是远程标签验收** |
| `make check`（Go/TS、Race、Lint、metadata、local cohort） | **PASS，exit 0** |
| `make coverage` | **PASS，exit 0**：Go statements 70.78%（门槛 70%），TS 87.64% |
| `make integration-test`：临时独立 PG16 / Redis8 动态端口 | **PASS，exit 0**；包含 stale wakeup / protected DLQ 真实服务回归；容器和网络已移除 |
| `make security` | **未通过，环境阻断（exit 2）**：`govulncheck` 到 `https://vuln.go.dev/index/modules.json.gz` 网络超时；不能当作扫描 PASS，也不能当作新漏洞 |
| 真实 GitHub tags、远程模块消费者、Beta GitHub Release | **NOT RUN / NO-GO**；Mac Pi 到 GitHub 本轮网络不可达，且无授权推送与发布 |

本地模块代理构建方式是临时验证工件，**未提交为公共发行物**。为了校验生成规则，将真实已经发布的 Core beta.22 Git 源构造成与 Go 发布代理相同的 ZIP（包含自动随模块附带的根 LICENSE），其模块校验和与既有 Go cache 中官方 ZIP 完全相同：

- beta.22 `h1:94ODuIqb26IVKpgvJhnenTmqyxldKKhJ7JTyGU9n54k=`（**完全一致**）
- 按同规则由现有新 Core 源文件构造的候选 beta.23：`h1:Y635o4hJQyo/WtCJMDI7bC/dPjtt/scy8wIaAbCVrDY=`，Go Mod checksum `h1:GCbTXGHuZK/u0SPm++VMuMYRJt1tY7vIEuXANKYRz0M=`。SQLite `go.sum` 锁定这组值。如在最终 tag 前修改 Core Module 的任何文件，**必须重新计算并验证此哈希**，不得绕过 mismatch。

本机日志：

- `/tmp/gaoge-sqlite-beta23-local-consumers.log`、`/tmp/gaoge-sqlite-beta23-unreplaced-consumers.log`
- `/tmp/gaoge-sqlite-beta23-unreplaced-module.log`、`/tmp/gaoge-sqlite-beta23-standalone-full.log`
- `/tmp/gaoge-sqlite-beta23-check.log`、`/tmp/gaoge-sqlite-beta23-coverage.log`
- `/tmp/gaoge-sqlite-beta23-security.log`、`/tmp/gaoge-sqlite-beta23-integration.log`

## 最终发布条件与复杂度约束

- **当前已解决：** 同仓 Core/SQLite API 版本协调、SQLite 作为十模块发行候选、独立消费者测试覆盖及真正不可变 tag 版本的发布时验证步骤。
- **明确未解决：** 未经授权的真实 GitHub 标签到位、被网络阻断的安全扫描，以及部署 Host/Provider、SQLite 移动真机与多 Profile 生产认证。这些不应被误称为 SQLite 模块自身功能 Conformance 回归。
- 不因这次新 SDK 发行候选而升级**未公开的 SQLite Schema v1**，不隐式迁移/删库、不添加新 Run 状态、Planner、独立协调存储或新 Kernel 模块。
- 发布前应重新联网验证预期 `beta.23` 尚无冲突标签，严格完成不可变标签及远程 `--published` 质量门。不能在本地报告中以源替换通过替代最终公开发行判断。

**收口裁决：SQLite 独立模块工程集成与无替换版本化构建 PASS；beta.23 官方发布和生产部署独立 NO-GO。**


## 合并前追加复核（2026-10-17，后续收口）

此段补充上述原始验收，不篡改原始命令与时间窗口；**没有改变 SDK 业务代码、Go Module 版本或 SQLite Schema**。

- **新通过：** `make integration-otel` 返回 **exit 0**，使用本机已存在的 `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.149.0`，trace/metric 四项 Collector 标志完成 E2E 验证；临时 Docker 容器和网络已清理。日志：`/tmp/gaoge-beta23-final-otel.log`。
- **混版负向证明：** 在带自动恢复 Trap 的临时本地测试中把 `go/agent-runtime-sqlite/go.mod` 的 Core 依赖由 beta.23 改为 beta.22，`node scripts/check-beta.mjs` **如期失败**并报告版本不一致；原文件立即恢复，未提交测试变更。日志：`/tmp/gaoge-beta23-negative-mixed-version.log`。
- **仍未通过：** 再次完整执行 `make security` 返回 **exit 2**，在 Go 漏洞 DB 初始索引 `https://vuln.go.dev/index/modules.json.gz` 连接超时；不是发现了新的可达代码漏洞，但也绝不能把此轮扫描记录为 PASS。日志：`/tmp/gaoge-beta23-recheck-security.log`。不得为了发布而跳过安全门禁。
- **GitHub 远程只读复核：** 通过已连接 GitHub 服务检查，`main` 相对 `44785f85d3117e9629c34ed48c7a6b458dbfbfab` **identical**；`governance/kernel-runtime` 无开放 PR；读取候选根标签 `v0.1.0-beta.23` 返回 GitHub 404（`No commit found for the ref`）。Mac Pi 本地至 GitHub 的 HTTPS 连通性仍异常，故以连接器的只读检查代替远程 fetch；**没有执行任何写操作**。
- **前序成功仍有效：** `make check=0`、`make coverage=0`、真实 PG16/Redis8 集成 exit 0、离线版本化 ZIP 的十模块 `--published` 无替换消费者 exit 0、SQLite `GOWORK=off GOFLAGS=-mod=readonly go test -race -count=1 ./...` exit 0。离线 ZIP 不构成 GitHub 真实标签的发布证明。

**最终合并/发布决策：** 已授权的 SQLite 独立模块**本地工程和发行候选准入 GO**；可在取得明确授权后 Push 分支、创建 PR 并等待远程必需检查。**直接合并 main、创建不可变标签和公开发布 beta.23 均未获授权，也尚未达到发布门禁**：待 GitHub PR 检查、安全扫描与实际标签消费者门禁。生产 Hosted/mobile/共享 Profile 不因本次 SDK 模块准入自动 GO。
