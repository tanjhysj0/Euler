# Euler Harbor 接入设计

- 日期：2026-11-19
- 状态：待实施
- 范围：把 Harbor 作为第二套编码能力评测执行层，与 euleval 并存

## 1. 目标

1. 用 Harbor 运行官方容器任务，评测 Euler 的真实编码能力。
2. 不下载全量数据集，只接入 1 个冒烟任务和 1 个编码任务。
3. Euler 保持宿主机运行，不把 Euler 与 PostgreSQL 容器化。
4. Euler 的任何改动都不影响现有 euleval 与 Go 单元测试。

## 2. 总体架构

```
宿主机 /tmp/euler-harbor/workspace  ←──bind mount──→  Harbor main 容器 /app
        ↑                                                    │
        │ Euler HTTP（session/run/SSE/自动审批）              │ tests/test.sh
   Euler 服务 127.0.0.1:8080 + PostgreSQL                    ↓
        ↑                                          /logs/verifier/reward.txt
        └──────── Python BaseAgent（Harbor 进程）
```

- Harbor 负责容器隔离、任务生命周期、隐藏测试上传与 reward 汇总。
- 外部 Python Agent 负责：初始化共享工作区、驱动 Euler、自动审批。
- 官方 `tests/test.sh` 保持原样，在修改后的 `/app` 上判分。

## 3. 目录

```
harbor_eval/
├── agents/
│   ├── __init__.py
│   ├── euler_client.py      # 纯 HTTP 客户端，不依赖 Harbor
│   └── euler_agent.py       # Harbor BaseAgent 适配器
├── tests/
│   ├── test_euler_client.py
│   └── test_helpers.py
├── jobs/
│   ├── hello-oracle.yaml
│   ├── hello-euler.yaml
│   ├── hello-nop.yaml
│   ├── coding-oracle.yaml
│   └── coding-euler.yaml
├── tasks/
│   ├── _downloads/          # 原始下载，gitignore
│   ├── hello-world/         # 适配后任务
│   └── cancel-async-tasks/  # 适配后任务
├── scripts/
│   ├── prepare-workspace.sh
│   ├── run-smoke.sh
│   └── run-coding.sh
└── results/                 # Harbor jobs_dir，gitignore
```

目录使用 `harbor_eval` 而不是 `harbor`，避免与已安装的 `harbor` Python 包同名冲突。

## 4. 任务适配

每个下载任务只做三处最小改造，官方 `tests/` 不改：

1. `Dockerfile` 末尾追加：
   ```dockerfile
   RUN mkdir -p /opt/baseline && cp -a <workdir>/. /opt/baseline/
   ```
   `<workdir>` 取 Dockerfile 最后一个 `WORKDIR`，缺省 `/app`。
2. `task.toml` 设置：
   ```toml
   [environment]
   workdir = "<workdir>"

   [verifier]
   environment_mode = "shared"
   user = "root"

   [agent]
   user = "root"
   ```
   Phase 1 使用 shared verifier，因为 bind mount 只在 Agent 容器中生效；
   Phase 2 再评估 artifacts + separate verifier。
3. 若任务缺少 `solution/solve.sh`，补一个可让 Oracle 通过的最小脚本。

bind mount 由 job 配置提供，不修改任务的 compose 文件：

```yaml
environment:
  type: docker
  mounts:
    - type: bind
      source: /tmp/euler-harbor/workspace
      target: <workdir>
```

## 5. 外部 Agent

`EulerAgent` 继承 `harbor.agents.base.BaseAgent`，实现 `setup` 与 `run`：

1. `setup`：`GET /health` 探测 Euler，最多重试 30 秒。
2. `run`：
   1. `environment.exec(command="pwd")` 获取容器工作目录。
   2. 清空该目录，并从 `/opt/baseline` 重新播种。
   3. 在宿主机对 `/tmp/euler-harbor/workspace` 执行 `git init` 与 baseline commit。
   4. 通过 `EulerClient` 创建/复用模型配置、创建会话（`path` 为宿主机工作区）、启动 Run。
   5. 消费 SSE；`approval.requested` 一律批准；断线用 `after=<seq>` 重连，最多 3 次。
   6. 超时取消 Run；终态非 `succeeded` 时抛错，让 Harbor 记为失败。
   7. 把 `run_id`、工具调用数、审批数写入 `AgentContext.metadata`。

指令由适配器在官方 `instruction` 前追加一行：

```
你的工作区目录是 /tmp/euler-harbor/workspace（原任务中的 <workdir> 对应此目录）。
```

不修改官方 `instruction.md`。

## 6. 任务选择

| 任务 | 用途 | 参考 |
|---|---|---|
| `hello-world/hello-world` | Harbor 接线冒烟 | 官方文档中的自定义 Agent 示例 |
| `terminal-bench/cancel-async-tasks` | 文件型编码任务 | Terminal-Bench 2.0，CPU-only |

编码任务候选按顺序尝试：`cancel-async-tasks`、`break-filter-js-from-html`、`fix-code-vulnerability`。
每个任务构建后检查镜像大小；两个任务镜像合计不得超过 4GB，超过则只保留 hello-world。

## 7. 安全与限制

- Harbor 并发固定为 1，因为宿主机工作区路径固定。
- `/tests` 在 Agent 结束后才上传，Euler 无法提前读取。
- Phase 1 使用 shared verifier；Euler 无法访问 `/tests`，但不能篡改 verifier 镜像。
- Euler shell 仍在宿主机执行，无沙箱；评测机应使用独立用户或虚拟机。
- 只支持文件型任务；依赖系统服务、GPU 或交互式网络的任务不纳入。
- 不提交任何真实 API Key；模型 Key 通过 Euler 服务进程环境变量提供。

## 8. 验收标准

1. `harbor run -c harbor_eval/jobs/hello-oracle.yaml` 得到 reward 1。
2. `harbor run -c harbor_eval/jobs/hello-euler.yaml` 得到 reward 1。
3. `harbor run -c harbor_eval/jobs/hello-nop.yaml` 得到 reward 0。
4. 编码任务 Oracle 得到 reward 1；EulerAgent 能产出 reward，且失败时不会误判为 1。
5. `uv run --with pytest --with httpx --with 'harbor==0.26.0' pytest harbor_eval/tests -q` 全绿。
6. 任务镜像合计 ≤ 4GB；`harbor_eval/results` 与 `/tmp/euler-harbor` 可清理。
7. 现有 Go 单元测试与 `golangci-lint` 不受影响。

## 9. 非目标

- 不下载全量数据集，不做多模型矩阵与并发评测。
- 不替换 euleval，不把 Euler/PostgreSQL 容器化。
- 不实现 separate verifier 与 artifacts（Phase 2）。
