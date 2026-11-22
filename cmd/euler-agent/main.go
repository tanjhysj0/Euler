// Command euler-agent 在无服务端环境下执行一次编码任务。
//
// 它不连接数据库，直接在当前工作区内驱动 Eino ReAct 循环，主要用于把
// agent 二进制打包进评测容器后原地运行：工作区就是容器工作目录，验证器
// 可以直接看到产物，无需宿主机映射或双向同步。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"euler/internal/agent"
	"euler/internal/approval"
	"euler/internal/config"
	"euler/internal/event"
	"euler/internal/run"
	"euler/internal/tools"
	"euler/internal/tracing"
)

const (
	version     = "0.1.0"
	exitFailure = 1
	minLimit    = 1
	// noRoundLimit 是轮次上限的“不设限”取值，此时只受运行时长约束。
	noRoundLimit        = 0
	noToolCallLimit     = 0
	dirPerm             = 0o755
	filePerm            = 0o600
	logKeyError         = "error"
	defaultMaxRounds    = noRoundLimit
	defaultMaxToolCalls = noToolCallLimit
	defaultMaxDuration  = 10 * time.Minute
	emptyInput          = ""

	statusSucceeded = "succeeded"
	statusFailed    = "failed"

	codeRunTimeout = "run_timeout"

	callRunning   = "running"
	callSucceeded = "succeeded"
	callFailed    = "failed"
	callRejected  = "rejected"

	promptDefault = "coding"
)

// options 汇总一次无服务端运行的全部参数。
type options struct {
	instruction     string
	instructionFile string
	dir             string
	resultPath      string
	prompt          string
	model           string
	baseURL         string
	apiKey          string
	reasoningEffort string
	maxRounds       int
	maxToolCalls    int
	maxToolOutput   int
	maxDuration     time.Duration
	shellTimeout    time.Duration
	modelTimeout    time.Duration
	previewBytes    int
}

// report 是写回调用方的运行结果，字段保持稳定以便评测侧解析。
type report struct {
	Version    string      `json:"version"`
	Status     string      `json:"status"`
	ErrorCode  string      `json:"error_code,omitempty"`
	Error      string      `json:"error,omitempty"`
	Output     string      `json:"output"`
	ToolCalls  []callEntry `json:"tool_calls"`
	DurationMS int64       `json:"duration_ms"`
	Model      string      `json:"model"`
	Workspace  string      `json:"workspace"`
}

// callEntry 描述一次工具调用的名称与终态。
type callEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func main() {
	showVersion := flag.Bool("version", false, "打印版本后退出")
	opts := bindFlags()
	flag.Parse()
	if *showVersion {
		if _, err := fmt.Fprintln(os.Stdout, "euler-agent "+version); err != nil {
			slog.Warn("write version", logKeyError, err)
		}
		return
	}
	if err := validate(*opts); err != nil {
		slog.Error("invalid flags", logKeyError, err)
		os.Exit(exitFailure)
	}
	rep := execute(*opts)
	writeReport(opts.resultPath, rep)
	if rep.Output != emptyInput {
		if _, err := fmt.Fprintln(os.Stdout, rep.Output); err != nil {
			slog.Warn("write output", logKeyError, err)
		}
	}
	if rep.Status != statusSucceeded {
		os.Exit(exitFailure)
	}
}

// bindFlags 绑定命令行参数；返回指针，确保 flag.Parse 写回同一份结构体。
func bindFlags() *options {
	defaults := config.Default()
	opts := options{dir: ".", maxRounds: defaultMaxRounds,
		maxToolCalls: defaultMaxToolCalls, maxToolOutput: defaults.ShellOutputBytes,
		maxDuration: defaultMaxDuration, shellTimeout: defaults.ShellTimeout,
		modelTimeout: defaults.ModelTimeout, previewBytes: defaults.PreviewBytes}
	flag.StringVar(&opts.instruction, "instruction", emptyInput, "任务指令文本")
	flag.StringVar(&opts.instructionFile, "instruction-file", emptyInput, "任务指令文件路径")
	flag.StringVar(&opts.dir, "dir", opts.dir, "工作区目录")
	flag.StringVar(&opts.resultPath, "result", emptyInput, "结果 JSON 输出路径")
	flag.StringVar(&opts.prompt, "prompt", promptDefault, "system prompt 模板名")
	flag.StringVar(&opts.model, "model", env("EULER_MODEL"), "模型名")
	flag.StringVar(&opts.baseURL, "base-url", env("EULER_MODEL_BASE_URL"), "模型 API 地址")
	flag.StringVar(&opts.reasoningEffort, "reasoning-effort",
		env("EULER_REASONING_EFFORT"), "推理强度：low/medium/high/max")
	flag.IntVar(&opts.maxRounds, "max-rounds", opts.maxRounds, "模型轮次上限（0 表示不设限）")
	flag.IntVar(&opts.maxToolCalls, "max-tool-calls", opts.maxToolCalls,
		"工具调用次数上限（0 表示不设限）")
	flag.IntVar(&opts.maxToolOutput, "tool-output-bytes", opts.maxToolOutput,
		"单次工具输出字节上限")
	flag.DurationVar(&opts.maxDuration, "max-duration", opts.maxDuration, "整次运行时长上限")
	flag.DurationVar(&opts.shellTimeout, "shell-timeout", opts.shellTimeout,
		"单条 shell 命令超时")
	flag.DurationVar(&opts.modelTimeout, "model-timeout", opts.modelTimeout,
		"单次模型请求超时")
	flag.IntVar(&opts.previewBytes, "preview-bytes", opts.previewBytes, "文件预览字节上限")
	// 密钥只从环境变量读取，避免出现在进程命令行里被其他进程看到。
	opts.apiKey = env("EULER_MODEL_API_KEY")
	if opts.apiKey == emptyInput {
		opts.apiKey = env("OPENAI_API_KEY")
	}
	return &opts
}

// validate 在真正启动前检查必需参数，避免把配置错误混进模型失败。
func validate(opts options) error {
	switch {
	case opts.instruction == emptyInput && opts.instructionFile == emptyInput:
		return errors.New("missing instruction: use -instruction or -instruction-file")
	case opts.model == emptyInput:
		return errors.New("missing model: use -model or EULER_MODEL")
	case opts.baseURL == emptyInput:
		return errors.New("missing base url: use -base-url or EULER_MODEL_BASE_URL")
	case opts.apiKey == emptyInput:
		return errors.New("missing api key: set EULER_MODEL_API_KEY or OPENAI_API_KEY")
	case opts.maxRounds < noRoundLimit:
		return errors.New("max rounds must be non-negative")
	case opts.maxToolCalls < noToolCallLimit:
		return errors.New("max tool calls must be non-negative")
	case opts.maxDuration < minLimit:
		return errors.New("duration limit must be positive")
	}
	return nil
}

// execute 执行一次 run 并汇总结果；任何失败都转成 report，不直接退出。
func execute(opts options) report {
	started := time.Now()
	rep := report{Version: version, Status: statusFailed, Model: opts.model,
		ToolCalls: []callEntry{}}
	instruction, dir, err := prepare(opts)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.Workspace = dir
	recorder := newRecorder()
	tracer := newTracer(config.LangfuseFromEnv())
	if tracer != nil {
		// 进程退出前刷新，确保最后一轮 trace 也上传
		defer tracer.Flush()
	}
	runtime := agent.NewRuntime(agent.Deps{
		Loader: agent.StaticLoader{Dir: dir, Prompt: opts.prompt,
			Model: agent.ModelSpec{BaseURL: opts.baseURL, Model: opts.model,
				APIKey: opts.apiKey},
			Calls: recorder, Messages: recorder},
		Events: noopPublisher{}, Gate: approval.AutoApprove{},
		Tracer: asRunTracer(tracer),
		Limits: config.AgentLimits{MaxModelRounds: opts.maxRounds,
			MaxToolCalls: opts.maxToolCalls, MaxDuration: opts.maxDuration,
			MaxOutputBytes: opts.maxToolOutput},
		ShellTimeout: opts.shellTimeout, ShellOutputBytes: opts.maxToolOutput,
		PreviewBytes: opts.previewBytes, ModelTimeout: opts.modelTimeout,
		ModelReasoningEffort: opts.reasoningEffort,
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, opts.maxDuration)
	defer cancel()
	err = runtime.Run(ctx, run.Request{RunID: uuid.New(), SessionID: uuid.New(),
		Input: instruction})
	rep.DurationMS = time.Since(started).Milliseconds()
	rep.Output = recorder.output()
	rep.ToolCalls = recorder.calls()
	if err != nil {
		rep.Error = err.Error()
		rep.ErrorCode = errorCode(err)
		return rep
	}
	rep.Status = statusSucceeded
	return rep
}

// prepare 解析指令与工作区，并校验工作区确实存在。
func prepare(opts options) (instruction, dir string, err error) {
	instruction = opts.instruction
	if opts.instructionFile != emptyInput {
		var data []byte
		data, err = os.ReadFile(opts.instructionFile)
		if err != nil {
			return emptyInput, emptyInput, fmt.Errorf("read instruction file: %w", err)
		}
		instruction = string(data)
	}
	if strings.TrimSpace(instruction) == emptyInput {
		return emptyInput, emptyInput, errors.New("instruction is empty")
	}
	dir, err = filepath.Abs(opts.dir)
	if err != nil {
		return emptyInput, emptyInput, fmt.Errorf("resolve workspace dir: %w", err)
	}
	var info os.FileInfo
	info, err = os.Stat(dir)
	if err != nil {
		return emptyInput, emptyInput, fmt.Errorf("stat workspace dir: %w", err)
	}
	if !info.IsDir() {
		return emptyInput, emptyInput, fmt.Errorf("workspace is not a directory: %s", dir)
	}
	return instruction, dir, nil
}

// errorCode 提取稳定的错误码；上下文超时单独归类为 run_timeout。
func errorCode(err error) string {
	var coded *tools.CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return codeRunTimeout
	}
	return emptyInput
}

// writeReport 把结果写入指定路径；未指定时跳过。
func writeReport(path string, rep report) {
	if path == emptyInput {
		return
	}
	data, err := json.MarshalIndent(rep, emptyInput, "  ")
	if err != nil {
		slog.Error("marshal report", logKeyError, err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		slog.Error("create result dir", logKeyError, err)
		return
	}
	if err = os.WriteFile(path, data, filePerm); err != nil {
		slog.Error("write report", logKeyError, err)
	}
}

// newTracer 在配置了 Langfuse 时创建追踪器，失败只降级关闭追踪。
func newTracer(cfg config.LangfuseConfig) *tracing.Tracer {
	if !cfg.Enabled() {
		return nil
	}
	tracer, err := tracing.New(tracing.Config{
		Host: cfg.Host, PublicKey: cfg.PublicKey, SecretKey: cfg.SecretKey,
		Environment: cfg.Environment,
	})
	if err != nil {
		slog.Warn("disable langfuse tracing", logKeyError, err)
		return nil
	}
	return tracer
}

// asRunTracer 把可能为 nil 的 tracer 转为接口，避免 typed nil 让判断失效。
func asRunTracer(tracer *tracing.Tracer) agent.RunTracer {
	if tracer == nil {
		return nil
	}
	return tracer
}

// recorder 在内存中记录工具调用与助手输出，供一次性运行汇总结果。
type recorder struct {
	mu      sync.Mutex
	indexes map[uuid.UUID]int
	entries []callEntry
	content string
}

func newRecorder() *recorder {
	return &recorder{indexes: make(map[uuid.UUID]int), entries: []callEntry{}}
}

// Start 记录一条开始执行的工具调用。
func (r *recorder) Start(_ context.Context, call tools.Call) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.indexes[call.ID] = len(r.entries)
	r.entries = append(r.entries, callEntry{Name: call.Name, Status: callRunning})
	return nil
}

// Succeed 标记工具调用成功。
func (r *recorder) Succeed(_ context.Context, id uuid.UUID, _ string) error {
	r.setStatus(id, callSucceeded)
	return nil
}

// Fail 标记工具调用失败。
func (r *recorder) Fail(_ context.Context, id uuid.UUID, _, _ string) error {
	r.setStatus(id, callFailed)
	return nil
}

// Reject 标记工具调用被审批拒绝。
func (r *recorder) Reject(_ context.Context, id uuid.UUID, _ string) error {
	r.setStatus(id, callRejected)
	return nil
}

// UpdateContent 保存最新助手文本；流式写入会多次调用，最后一次即全文。
func (r *recorder) UpdateContent(_ context.Context, _ uuid.UUID, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content = content
	return nil
}

func (r *recorder) setStatus(id uuid.UUID, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index, ok := r.indexes[id]; ok {
		r.entries[index].Status = status
	}
}

func (r *recorder) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.content
}

// calls 返回工具调用快照的副本，避免调用方与写入方共享切片。
func (r *recorder) calls() []callEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]callEntry{}, r.entries...)
}

// noopPublisher 实现事件端口但不持久化事件；无服务端运行时没有订阅者。
type noopPublisher struct{}

func (noopPublisher) Append(
	_ context.Context, runID uuid.UUID, eventType string, _ any,
) (event.Event, error) {
	return event.Event{RunID: runID, Type: eventType}, nil
}

func (noopPublisher) ListAfter(
	context.Context, uuid.UUID, int64,
) ([]event.Event, error) {
	return nil, nil
}

func (noopPublisher) Subscribe(uuid.UUID) (<-chan event.Event, func()) {
	channel := make(chan event.Event)
	close(channel)
	return channel, func() {}
}

// env 读取环境变量，未设置时返回空字符串。
func env(name string) string { return os.Getenv(name) }
