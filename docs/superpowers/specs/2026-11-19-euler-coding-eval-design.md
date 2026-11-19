# Euler 编码能力评测系统（euleval）设计

- 日期：2026-11-19
- 状态：待用户审阅
- 范围：第一阶段，只评测"编码能力"（改文件、修 Bug）

## 1. 背景与目标

`如何测试agent.md` 给出的核心原则：测写代码 Agent 不能只看 diff 像不像，必须在隔离环境中**打上补丁后，原本失败的测试（FAIL_TO_PASS）是否变绿，原本通过的测试（PASS_TO_PASS）有没有被搞崩**。

本设计把该原则落成一个可执行的工具 `euleval`：

1. 黑盒驱动真实的 Euler 服务，让 Agent 在隔离工作区里读代码、改代码、跑测试、修 Bug。
2. 严格分离**推理阶段**（Runner）与**判分阶段**（Judge），判分器不信任 Agent 的任何输出。
3. 输出可复现的 `preds.jsonl` 与 `report.json`，给出 Resolved%、逐测试结果与工程指标。
4. 提供 `smoke` 模式：用参考补丁（gold）走判分流水线，**不需要 Euler 服务、PostgreSQL、模型 Key** 即可验证判分器自身可信。

## 2. 已确认决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 驱动方式 | 黑盒 HTTP API | 测真实产品链路（会话、Run、SSE、审批），评测代码零侵入业务代码 |
| 任务仓库 | 专用 fixture Go 仓库 | 可复现、纯本地、与判分器隔离，题目可持续积累 |
| 代码落点 | `eval/` 独立 go module | 与主模块依赖隔离，可单独演进 |
| 题目存储 | 方案 A：干净 fixture + 每题三个补丁 | 文本可 review、无二进制、基线确定性复现 |

## 3. 非目标（第一阶段不做）

- 不接入 SWE-bench Verified 全量数据集，不做跨语言任务。
- 不做 LLM-as-a-Judge、不做对话质量/主观评分。
- 不做容器级沙箱；隔离依赖临时目录中的独立 git 仓库。
- 不做 Web UI，只做 CLI 与 JSON/控制台报告。
- 不做多模型对比矩阵、多次重复采样（数据结构预留字段，暂不实现）。

## 4. 与 Euler 现有能力的关系

评测只依赖已存在的公开 API（已核实到代码）：

| 用途 | 端点 / 位置 |
|---|---|
| 健康检查 | `GET /health`（[router.go](file:///Users/jason/go/src/Euler/internal/api/router.go#L34-L41)） |
| 获取本机 cookie | `GET /api/v1/bootstrap`（[middleware.go](file:///Users/jason/go/src/Euler/internal/api/middleware.go#L46-L52)） |
| 创建模型配置 | `POST /api/v1/model-profiles`，`secret_kind=environment` |
| 创建会话并绑定目录 | `POST /api/v1/sessions`，`{profile_id, path}`（[handler.go](file:///Users/jason/go/src/Euler/internal/session/handler.go#L90-L111)） |
| 启动 Run | `POST /api/v1/sessions/:id/runs`，`{content}`（[handler.go](file:///Users/jason/go/src/Euler/internal/run/handler.go#L42-L64)） |
| 订阅事件 | `GET /api/v1/runs/:id/events`，支持 `?after=`/`Last-Event-ID` 续传 |
| 查询快照 | `GET /api/v1/runs/:id`（含 run 状态、tool_calls、approvals） |
| 审批决策 | `POST /api/v1/approvals/:id/decision`，`{approved, reason}` |
| 读取消息 | `GET /api/v1/sessions/:id/messages` |

关键事实：

- 创建会话时传入绝对路径即绑定该目录，Runner 可直接把临时 git 仓库交给 Agent。
- `file_write/file_patch/file_delete/shell` 需要审批；Runner 监听 `approval.requested` 并立即批准，实现无人值守。
- Run 终态事件：`run.completed` / `run.failed` / `run.cancelled` / `run.interrupted`。
- 模型配置默认上限：12 轮模型、40 次工具调用、15 分钟 Run 时长（[config.go](file:///Users/jason/go/src/Euler/internal/config/config.go#L15-L27)），评测超时不得小于该值。

## 5. 总体架构

```
                ┌──────────────────── euleval ────────────────────┐
                │                                                 │
 fixture/ ──────┼─→ task 加载 → workspace 物化(buggy 基线)         │
 tasks/*.json   │                     │                           │
                │                     ├─→ runner ──HTTP/SSE──→ euler 服务 ──→ Agent
                │                     │      ↓                    │
                │                     │   preds.jsonl             │
                │                     │      ↓                    │
                │                     └─→ judge（全新物化）         │
                │                          model_patch + test_patch│
                │                          go test -json           │
                │                            ↓                    │
                │                         report.json               │
                └─────────────────────────────────────────────────┘
```

原则：

1. **判分器不使用 Agent 的工作区**。Judge 从 fixture 重新物化一份干净基线再打补丁，Agent 留下的任何未提交状态都不会被采信。
2. **隐藏测试从不进入 Agent 工作区**。`test.patch` 只在 Judge 阶段应用。
3. **评测器零依赖业务代码**。`eval/` 不 import `euler/internal/...`，只做 HTTP 客户端与子进程调用。

## 6. 目录结构

```
eval/
├── go.mod                     # module euler/eval，仅标准库
├── .golangci.yml              # 独立 module 根配置，复用主仓基线
├── Makefile                   # test / lint / smoke
├── cmd/euleval/main.go        # CLI：tasks / run / judge / smoke
├── internal/
│   ├── task/                  # 题目加载、校验、按 id 过滤
│   ├── workspace/             # 物化 fixture、git init、打补丁、提取 diff
│   ├── runner/                # Euler HTTP 客户端 + SSE 事件循环 + 自动审批
│   ├── judge/                 # 物化、补丁应用、go test -json 解析、判定
│   └── report/                # preds.jsonl 读写、report.json、控制台摘要
├── fixture/                   # 小型 Go 练习仓库（独立 module）
│   ├── go.mod                 # module example.com/euler-fixture
│   ├── .golangci.yml
│   ├── textutil/
│   ├── calculator/
│   ├── store/
│   └── state/
└── tasks/
    └── <task_id>/
        ├── task.json
        ├── bug.patch          # 注入 Bug（相对 fixture 根）
        ├── test.patch         # 隐藏测试
        └── gold.patch         # 参考修复
```

## 7. 题库格式

`tasks/<task_id>/task.json`：

```json
{
  "id": "textutil-001",
  "title": "Split 在空分隔符下 panic",
  "issue": "调用 textutil.Split(\"a,b\", \"\") 会直接 panic。期望与 strings.Split 一致：按空分隔符切成单个字符。请修复并保持现有行为不变。",
  "difficulty": "easy",
  "tags": ["edge-case", "panic"],
  "protected_tests": ["textutil/split_test.go"],
  "fail_to_pass": ["TestSplitEmptySep", "TestSplitEmptySepUnicode"],
  "pass_to_pass": ["TestSplitBasic", "TestSplitEmptyInput"],
  "test_command": "go test -count=1 -json ./...",
  "timeout_seconds": 900
}
```

字段约束：

- `id` 必须与目录名一致。
- `issue` 是喂给 Agent 的唯一需求文本，**不得泄露隐藏测试名或断言细节**。
- `protected_tests` 是 Judge 必须还原为基线的测试文件路径（防篡改）。
- `fail_to_pass` / `pass_to_pass` 使用 Go 测试名（不含包名）；同名测试跨包时必须用 `包路径::测试名` 区分。
- `test_command` 缺省为 `go test -count=1 -json ./...`；并发相关题目可设为 `go test -race -count=1 -json ./...`。自定义时**必须包含 `-json`**，否则 `tasks validate` 直接报错。
- `timeout_seconds` 缺省 900，且不得小于 Euler 侧的 Run 上限。

`euleval tasks validate` 负责校验：JSON 合法、三个补丁存在且能依次应用到 fixture、F2P/P2P 非空且不相交、受保护测试文件存在于基线。

## 8. fixture 仓库设计

一个刻意做小的、纯标准库的 Go module（`example.com/euler-fixture`），用于承载多类 Bug：

| 包 | 形态 | 可出题方向 |
|---|---|---|
| `textutil` | 字符串切分 / 转义 | 边界条件、off-by-one、Unicode |
| `calculator` | 表达式求值（手写递归下降） | 解析错误、错误处理遗漏 |
| `store` | 并发安全的内存 KV | 数据竞争、锁粒度、迭代时删除 |
| `state` | 订单状态机 | 非法迁移、状态遗漏 |

约束：

- 只依赖标准库，`go build ./...` 与 `go test ./...` 在离线环境可完成，单题测试总时长 < 5 秒。
- 每个包预置**可见测试**，这些测试构成 PASS_TO_PASS 的天然来源。
- fixture 自身不含 Bug；Bug 一律通过 `bug.patch` 注入，保证 fixture 是"干净模板"。
- fixture 不包含 `.git`；物化时新建仓库，避免嵌套仓库污染主仓。
- 首批 5 道题：边界条件 1、错误处理 1、并发（`-race`）1、解析逻辑 1、状态机 1。

## 9. 工作区物化（workspace 包）

`Materialize(fixtureDir, dstDir, patchLists ...string) (baselineSHA string, err error)`：

1. 递归复制 fixture 到 `dstDir`（排除 `.git`、`tasks`）。
2. `git init -q`，设置仓库级 `user.name=euler-eval`、`user.email=eval@local`（不触碰全局 git config）。
3. `git add -A && git commit -q -m baseline`。
4. 依次 `git apply --check` 再 `git apply` 每个补丁，每批后提交一次。
5. 返回基线 commit SHA（Runner 用来提取 diff；Judge 用来还原受保护测试）。

补充约定：

- 默认工作根 `--work-dir`，缺省为系统临时目录下的 `euleval-<时间戳>`；`--keep-workdir` 可保留现场用于排查。
- 物化后工作区**不包含任何隐藏测试**，Agent 只能看到 `bug.patch` 注入后的可见测试。
- `git apply` 失败一律视为配置错误（题目与 fixture 不匹配），直接终止，不静默跳过。

## 10. Runner（推理阶段）

### 10.1 前置检查

1. `GET /health` 探测就绪（最多重试 30 秒）。
2. `GET /api/v1/bootstrap`，把 `euler_local` cookie 存入 `http.CookieJar`；请求不携带 `Origin` 头（同源检查放行）。
3. 模型配置：
   - 指定 `--profile-id` 时直接使用；
   - 否则 `POST /model-profiles` 创建 `{name:"euleval-<时间戳>", base_url, model, secret_kind:"environment", secret_ref:<环境变量名>}`。
   - **注意**：`environment` 类型读取的是 **Euler 服务进程**的环境变量，因此启动 euler 时需带上该变量（运行手册写入 CLI `-h`）。

### 10.2 单题流程

```
materialize(buggy 基线) → POST /sessions {profile_id, path}
→ POST /sessions/:id/runs {content: issue} → 拿 run_id
→ GET /runs/:id/events (SSE)
     ├─ approval.requested → POST /approvals/:aid/decision {approved:true, reason:"euleval"}
     └─ run.completed / failed / cancelled / interrupted → 退出
→ GET /runs/:id 快照（tool_calls、耗时）
→ GET /sessions/:id/messages（仅存 transcript，供诊断）
→ git add -A && git diff --cached --binary <baseline_sha>  → model_patch
→ 清理工作区（除非 --keep-workdir）
```

细节：

- **每个任务新建会话**，避免上下文串味与"该会话已有活跃 Run"冲突。
- **自动审批无条件放行**：评测关心 Agent 能否完成修复，不评测审批策略；审批次数计入指标。
- **超时控制**：`min(task.timeout_seconds, --run-timeout)`，超时后调 `POST /runs/:id/cancel`，该题记 `timeout`。
- **SSE 断线**：记录最后 `seq`，用 `?after=<seq>` 重连，最多 3 次；仍失败则退化为每 2 秒轮询 `GET /runs/:id`。
- **补丁提取**：`git add -A` 把未跟踪文件纳入，`git diff --cached` 与基线比较，因此 Agent 无论是直接改工作区还是自己 `git commit` 都能被完整捕获。`.git` 被破坏导致提取失败记 `error`，不得当作空补丁静默通过。
- 补丁大小上限 2 MiB，超过记 `patch_too_large`。

### 10.3 失败分类

| 状态 | 含义 |
|---|---|
| `completed` | Run 正常结束，已提取补丁 |
| `failed` | Run 失败（含模型错误、超过轮次/工具上限） |
| `timeout` | Runner 主动取消 |
| `error` | 评测器自身错误（服务不可达、git 异常等） |

任何单题失败都不中断整批；所有结果仍写入 `preds.jsonl`。

## 11. Judge（判分阶段）

### 11.1 单题流程

1. 重新 `Materialize(fixture, 新临时目录, bug.patch)`，得到干净 buggy 基线。
2. `git apply --3way <model_patch 文件>`（以 buggy 基线为工作树）；失败记 `patch_failed`（这本身是能力信号）。
3. **防篡改还原**：
   - 对 `protected_tests` 中每个文件执行 `git checkout <baseline_sha> -- <file>`，还原 Agent 可能改动的可见测试；
   - 删除 Agent 新增的、基线中不存在的 `*_test.go`，避免其编译错误污染判分；
   - 最后应用 `test.patch` 注入隐藏测试（隐藏测试可修改受保护文件，顺序必须在还原之后）。
4. `go build ./...`，失败则 `resolved=false`（原因 `build_failed`）。
5. 执行 `test_command`，按行解析 `go test -json` 的 `TestEvent`。
6. 判定：
   - `fail_to_pass` 中每个测试必须出现且 `Action == "pass"`；
   - `pass_to_pass` 中每个测试必须出现且 `Action == "pass"`；
   - 缺失、`skip`、包级 `fail` 都算不通过。
7. 清理临时目录，写入结果。

### 11.2 判分不变量

- Judge 永不读取 Agent 工作区里的产物，只读 `preds.jsonl` 中的补丁文本。
- 隐藏测试不参与 Runner，Agent 无法针对断言特化。
- 判分结果只由测试通过情况与构建结果决定，不做 diff 风格打分。

## 12. 指标与报告

### 12.1 preds.jsonl（Runner 输出）

```json
{
  "instance_id": "textutil-001",
  "model_name_or_path": "euler-local",
  "model_patch": "diff --git a/... ",
  "meta": {
    "status": "completed",
    "run_id": "…",
    "duration_ms": 183420,
    "tool_calls": 17,
    "approvals": 6,
    "diff_files": 2,
    "diff_added": 24,
    "diff_deleted": 6,
    "touched_test_files": false,
    "error": ""
  }
}
```

### 12.2 report.json（Judge 输出）

```json
{
  "started_at": "2026-11-19T15:00:00+08:00",
  "finished_at": "2026-11-19T15:04:12+08:00",
  "totals": {
    "tasks": 5, "resolved": 3, "resolved_rate": 0.6,
    "patch_failed": 1, "no_changes": 0, "build_failed": 1
  },
  "tasks": [
    {
      "instance_id": "textutil-001",
      "resolved": true,
      "fail_to_pass": {"total": 2, "passed": 2, "failed": []},
      "pass_to_pass": {"total": 2, "passed": 2, "failed": []},
      "build_ok": true,
      "metrics": {"duration_ms": 183420, "tool_calls": 17, "diff_files": 2}
    }
  ]
}
```

控制台摘要按题输出一行：`PASS/FAIL  题目  F2P 2/2  P2P 2/2  耗时  工具调用数`。

工程指标（第一阶段只记录，不进入判定）：

- diff 文件数与增删行数（改动收敛度）。
- 是否触碰 `*_test.go`（触碰仍可通过，因为 Judge 会还原，但指标单独暴露"作弊倾向"）。
- Run 时长、工具调用次数、审批次数。

## 13. CLI 设计

```
euleval tasks validate --tasks ./tasks
euleval run   --base-url http://127.0.0.1:8080 --tasks ./tasks --out ./results/run-<ts>
              [--profile-id UUID | --model-base-url URL --model NAME --api-key-env VAR]
              [--model-name euler-local] [--only id1,id2]
              [--work-dir DIR] [--run-timeout 15m] [--keep-workdir]
euleval judge --tasks ./tasks --preds ./results/run-<ts>/preds.jsonl --out ./results/run-<ts>
euleval smoke --tasks ./tasks --out ./results/smoke
```

- `run` 只负责产出 `preds.jsonl` 与 `transcripts/`，不判分。
- `judge` 只读 `preds.jsonl` 与题库，产出 `report.json` 与控制台摘要。
- `smoke` 不依赖 Euler 服务：用 `gold.patch` 当作 `model_patch` 走 Judge，全部题目必须 resolved，退出码非零即失败。
- `--only` 支持日常小样本回归；`--model_name_or_path` 用于在 `preds.jsonl` 中标记被测版本（缺省取 profile 的 model 名）。

## 14. 错误处理

- 服务不可达、cookie 获取失败：启动阶段立即失败并给出明确提示，不产生半截报告。
- 单题内部错误：记录到 `meta.error`，继续下一题。
- 审批重复决策返回 409：忽略。
- 题目补丁无法应用：`tasks validate` 阶段失败；Judge 阶段应用 Agent 补丁失败记 `patch_failed`。
- `go test -json` 解析：非 JSON 行忽略，仅解析 `TestEvent`；出现 `build failed` 包级事件时按构建失败处理。

## 15. 安全与隔离

- Agent 在临时目录中的独立 git 仓库工作，`path` 由 Runner 指定，不落在用户真实项目里。
- 隐藏测试与 gold 补丁始终在 `eval/` 目录，且不会复制进 Agent 工作区。
- 已知风险：Euler 的 shell 工具在宿主机执行且无沙箱，Agent 理论上可访问工作区之外的文件。本机单用户场景接受该风险；评测机器建议使用独立用户或虚拟机。
- 评测结束后删除临时目录，避免残留 Agent 产物。

## 16. 测试策略

- `internal/task`：非法 JSON、缺补丁、F2P/P2P 重叠等用例。
- `internal/workspace`：物化后 `git status` 干净、补丁可应用、误用错误补丁时报错。
- `internal/judge`：用 `testdata/` 下的迷你 module 验证 F2P/P2P 判定、篡改测试被还原、Agent 新增测试被清除、构建失败分支。
- `internal/runner`：用 `httptest` 实现假 Euler 服务（bootstrap/sessions/runs/SSE/approvals），验证自动审批、事件终态、超时取消、断线重连与补丁提取。
- 全部单测不依赖 PostgreSQL 与外部模型。
- lint 门槛：`cd eval && golangci-lint run --timeout=5m ./...` 零 issue。

## 17. 验收标准

1. `cd eval && go test ./...` 全绿，无需 PG 与模型 Key。
2. `cd eval && golangci-lint run --timeout=5m ./...` 零 issue。
3. `euleval smoke --tasks ./tasks` 输出 5/5 resolved，退出码 0。
4. 对真实 Euler 服务执行 `run` + `judge` 全流程跑通：产生 `preds.jsonl`、`report.json`，并验证"空补丁"必然判为 unresolved。
5. Judge 单题耗时 < 30 秒；单题端到端耗时不超过配置的 Run 超时。

## 18. 风险与后续

| 风险 | 缓解 |
|---|---|
| Euler 启动依赖 PostgreSQL | `smoke` 模式完全不需要服务；`run` 文档要求先 `docker compose up -d postgres` |
| 模型 Key 需在 Euler 进程环境中 | CLI `-h` 明确说明；也支持复用已存在的 `--profile-id` |
| Agent 非确定性，单次结果波动 | 已在报告结构中预留多次运行聚合；第一阶段先跑单次，靠题库扩充提升区分度 |
| fixture 题目过拟合 | 后续从真实 Bugfix 归档三件套（基线 + Issue + 测试补丁）持续扩充题库 |
| 题库与 fixture 漂移 | `euleval tasks validate` 作为出题门禁，任何补丁失效立刻暴露 |
