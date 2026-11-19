测“写代码/修 Bug 的 Agent”和测业务流程 Agent 的核心差异在于：**真刀真枪在代码仓库里改文件、跑构建、跑单元测试**。你不能只看它生成的 diff 像不像，必须看它在隔离环境中**打上补丁后，原本失败的测试（FAIL_TO_PASS）是否变绿，原本通过的测试（PASS_TO_PASS）有没有被搞崩**。

把 **SWE-bench Verified** 融入你的测试体系，最务实的做法不是把 500 道题全拿来当日常回归，而是**把它当“通用编码能力标尺 + 标准化评测底座”，再叠加你自己的“私有仓库任务集”**。

---

### 一、 测写代码 Agent 的核心流水线（Inference + Harness 分离）

工程上，写代码 Agent 的评测严格分成两段（不要混在一起）：

1. **推理阶段（Agent Runner）**：
* 给 Agent 一个隔离的工作区（通常是独立容器或 git worktree）和一条 Issue/需求描述。
* Agent 自己去搜代码、读文件、改代码、跑局部测试，最后输出一份标准 `git diff`（即 `model_patch`），写入 `preds.jsonl`。


2. **判分阶段（Evaluation Harness）**：
* 拿干净的基础仓库镜像 -> 打入 `model_patch` -> 注入官方/隐藏的测试补丁 -> 跑测试套件 -> 解析测试日志看是否通过。
* **SWE-bench 官方 Harness** 做的正是第 2 步（以及镜像构建/缓存管理）。



---

### 二、 怎么接入 SWE-bench Verified？（一条命令跑通）

**SWE-bench Verified** 是从原 SWE-bench 中经人工校验挑出的 **500 道高质量真实 GitHub Issue 修复题**（主要是知名 Python 开源库），排除了很多环境烂、题意不清或单测有缺陷的“脏题”。

#### 1. 环境准备（避坑提醒）

* 机器配置建议：**至少 120GB 剩余磁盘、16GB+ 内存、8 核以上 CPU**（镜像层和日志很吃硬盘，跑完记得及时清理无用镜像）。
* 安装官方包并启动 Docker：
```bash
git clone https://github.com/SWE-bench/SWE-bench.git
cd SWE-bench && pip install -e .

```



#### 2. 先用 `--predictions_path gold` 跑通冒烟校验

在接你的 Agent 之前，先跑官方参考补丁（`gold`）验证 Docker 和评测器是否正常：

```bash
python -m swebench.harness.run_evaluation \
  --dataset_name SWE-bench/SWE-bench_Verified \
  --predictions_path gold \
  --instance_ids sympy__sympy-20590 \
  --max_workers 1 \
  --run_id smoke-gold

```

*(注意：Harness 会按 `run_id` 和 `instance_id` 做结果缓存，下次换了新补丁想重测同一题，一定要换一个新的 `--run_id`，否则会直接读缓存。)*

#### 3. 让你的 Agent 生成 `preds.jsonl` 并一键评测

你的 Agent 跑完任务后，按行输出如下格式的 `preds.jsonl`：

```json
{"instance_id": "sympy__sympy-20590", "model_name_or_path": "my-coding-agent-v1", "model_patch": "diff --git a/... b/...\n..."}

```

然后一条命令跑评测：

```bash
python -m swebench.harness.run_evaluation \
  --dataset_name SWE-bench/SWE-bench_Verified \
  --predictions_path ./preds.jsonl \
  --max_workers 8 \
  --run_id agent-v1-run1

```

---

### 三、 如何融入你的“企业/个人真实代码测试”？

SWE-bench Verified 测的是开源 Python 仓库的 Bug 修复能力，但**无法替代你自己的业务代码库**（你的技术栈可能是 Go/Flutter/React/TypeScript，代码风格和内部微服务也不同）。企业里通常这样组合：

1. **分层使用，控制成本与耗时**：
* **日常改 Prompt / 改工具（高频）**：只抽 **20~30 条** SWE-bench Verified 子集（按难度/仓库分层抽样）+ **20 条** 自己仓库的典型任务，几分钟跑完看方向。
* **重大版本升级 / 换基座模型（低频）**：跑全量 500 条 **SWE-bench Verified** + 全量私有代码任务集，对比 `Resolved %`、平均步数、Token 成本和单题耗时。


2. **把你们自己的 Git 历史变成“私有 SWE-bench”**：
从你们自己的代码仓库挑出过去半年的真实 Bugfix PR，记录三样东西即可组成一条私有任务：
* `base_commit`（修 Bug 前的提交号）
* 问题描述 / Issue 文本（脱敏后）
* `test_patch`（PR 里新增或修改的单元测试文件 `git diff`）
评测时同样是：`checkout base_commit` -> 应用 Agent 的 `model_patch` -> 应用 `test_patch` -> 跑你们的测试命令（如 `go test ./...`、`pytest`、`flutter test`）。


3. **补上代码 Agent 特有的“工程质量指标”**：
单测全过不代表代码写得漂亮，建议再加几项硬检查：
* **改动收敛度（Diff 大小）**：是否顺手乱改了几十个无关文件或删掉了原有断言/测试？
* **Lint / 类型检查**：改完后 `ruff` / `mypy` / `tsc` / `golangci-lint` 是否零报错？
* **超时与死循环率**：是否在限定步数或超时时间内正常结束。
