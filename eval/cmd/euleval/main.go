// Command euleval 是 Euler 编码能力评测工具的入口。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"euler/eval/internal/judge"
	"euler/eval/internal/report"
	"euler/eval/internal/runner"
	"euler/eval/internal/task"
	"euler/eval/internal/workspace"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "tasks":
		err = runTasks(os.Args[2:])
	case "run":
		err = runRun(os.Args[2:])
	case "judge":
		err = runJudge(os.Args[2:])
	case "smoke":
		err = runSmoke(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "euleval: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: euleval <tasks|run|judge|smoke> [flags]")
}

func runTasks(args []string) error {
	if len(args) == 0 || args[0] != "validate" {
		return fmt.Errorf("用法: euleval tasks validate --tasks DIR --fixture DIR")
	}
	fs := flag.NewFlagSet("tasks validate", flag.ContinueOnError)
	tasksDir := fs.String("tasks", "./tasks", "题库目录")
	fixtureDir := fs.String("fixture", "./fixture", "fixture 仓库目录")
	only := fs.String("only", "", "只校验指定题目，逗号分隔")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tasks, err := task.Load(*tasksDir)
	if err != nil {
		return err
	}
	ids := splitIDs(*only)
	tasks, err = task.Filter(tasks, ids)
	if err != nil {
		return err
	}
	workRoot, err := os.MkdirTemp("", "euleval-validate-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workRoot) }()
	for _, item := range tasks {
		if err = item.ValidateBasic(); err != nil {
			return err
		}
		if err = task.ValidatePatches(item, *fixtureDir, workRoot); err != nil {
			return err
		}
		fmt.Printf("OK   %s\n", item.ID)
	}
	fmt.Printf("校验完成：%d 道题\n", len(tasks))
	return nil
}

// runRun 执行推理阶段，逐题追加 preds.jsonl。
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	baseURL := fs.String("base-url", "http://127.0.0.1:8080", "Euler 服务地址")
	tasksDir := fs.String("tasks", "./tasks", "题库目录")
	fixtureDir := fs.String("fixture", "./fixture", "fixture 仓库目录")
	outDir := fs.String("out", "./results/run", "输出目录")
	profileID := fs.String("profile-id", "", "复用的模型配置 ID")
	modelBaseURL := fs.String("model-base-url", "", "模型 base_url")
	modelName := fs.String("model", "", "模型名")
	apiKeyEnv := fs.String("api-key-env", "", "模型 Key 所在环境变量名")
	label := fs.String("model-name", "", "写入 preds 的被测版本名")
	only := fs.String("only", "", "只跑指定题目，逗号分隔")
	runTimeout := fs.Duration("run-timeout", 15*time.Minute, "单题超时")
	workDir := fs.String("work-dir", "", "工作区根目录，缺省为临时目录")
	keepWorkdir := fs.Bool("keep-workdir", false, "保留工作区")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *apiKeyEnv != "" {
		fmt.Fprintf(os.Stderr,
			"提示: environment 类型密钥读取 Euler 服务进程的环境变量，请确认该进程已设置 %s\n",
			*apiKeyEnv)
	}
	items, err := loadSelected(*tasksDir, *only)
	if err != nil {
		return err
	}
	client, err := runner.NewClient(context.Background(), runner.Options{
		BaseURL: *baseURL, ProfileID: *profileID, ModelBaseURL: *modelBaseURL,
		Model: *modelName, APIKeyEnv: *apiKeyEnv, ModelName: *label,
		RunTimeout: *runTimeout,
	})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*outDir, 0o755); err != nil { //nolint:gosec // 输出目录需可读执行
		return err
	}
	workRoot := *workDir
	if workRoot == "" {
		workRoot, err = os.MkdirTemp("", "euleval-run-*")
		if err != nil {
			return err
		}
		if !*keepWorkdir {
			defer func() { _ = os.RemoveAll(workRoot) }()
		}
	}
	predsPath := filepath.Join(*outDir, "preds.jsonl")
	if err = os.Remove(predsPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	completed := 0
	failed := make([]string, 0)
	for _, item := range items {
		if err = item.ValidateBasic(); err != nil {
			return err
		}
		dst := filepath.Join(workRoot, item.ID)
		repo, materializeErr := workspace.Materialize(*fixtureDir, dst, item.BugPatch())
		if materializeErr != nil {
			return materializeErr
		}
		pred := client.RunTask(context.Background(), item, repo)
		if err = report.AppendPrediction(predsPath, pred); err != nil {
			return err
		}
		if pred.Meta.Status == "error" {
			// 单题推理失败只记录警告，不中断整批。
			failed = append(failed, item.ID)
			fmt.Fprintf(os.Stderr, "警告: 推理 %s 失败，status=error，继续下一题\n", item.ID)
		} else {
			completed++
		}
		fmt.Printf("完成 %-20s status=%s\n", item.ID, pred.Meta.Status)
		if !*keepWorkdir {
			_ = os.RemoveAll(dst)
		}
	}
	fmt.Printf("推理完成 %d/%d 题，preds 写入 %s\n", completed, len(items), predsPath)
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "推理失败题目: %s\n", strings.Join(failed, ", "))
	}
	return nil
}

// runJudge 执行判分阶段。
func runJudge(args []string) error {
	fs := flag.NewFlagSet("judge", flag.ContinueOnError)
	tasksDir := fs.String("tasks", "./tasks", "题库目录")
	fixtureDir := fs.String("fixture", "./fixture", "fixture 仓库目录")
	predsPath := fs.String("preds", "", "preds.jsonl 路径")
	outDir := fs.String("out", "", "输出目录")
	keepWorkdir := fs.Bool("keep-workdir", false, "保留判分工作区")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *predsPath == "" {
		return fmt.Errorf("必须提供 --preds")
	}
	if *outDir == "" {
		*outDir = filepath.Dir(*predsPath)
	}
	items, err := task.Load(*tasksDir)
	if err != nil {
		return err
	}
	byID := make(map[string]task.Task, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	preds, err := report.ReadPredictions(*predsPath)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(preds))
	for _, pred := range preds {
		if _, ok := seen[pred.InstanceID]; ok {
			return fmt.Errorf("preds 中存在重复的 instance_id: %s", pred.InstanceID)
		}
		seen[pred.InstanceID] = struct{}{}
	}
	workRoot, err := os.MkdirTemp("", "euleval-judge-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workRoot) }()
	value := report.Report{StartedAt: time.Now().Format(time.RFC3339)}
	results := make([]report.TaskResult, 0, len(preds))
	for _, pred := range preds {
		item, ok := byID[pred.InstanceID]
		if !ok {
			return fmt.Errorf("preds 中存在未知题目: %s", pred.InstanceID)
		}
		var judged judge.Result
		if strings.TrimSpace(pred.ModelPatch) == "" {
			judged = judge.Result{InstanceID: item.ID, Reason: "no_changes"}
		} else {
			judged, err = judge.Run(item, pred.ModelPatch, judge.Options{
				FixtureDir: *fixtureDir, WorkRoot: workRoot, KeepWorkdir: *keepWorkdir,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "警告: 判分 %s 失败，记为 error: %v\n", item.ID, err)
				results = append(results, report.TaskResult{
					InstanceID: item.ID, Reason: "error",
					Metrics: report.Metrics{
						DurationMS: pred.Meta.DurationMS,
						ToolCalls:  pred.Meta.ToolCalls,
						DiffFiles:  pred.Meta.DiffFiles,
					},
				})
				continue
			}
		}
		results = append(results, report.TaskResult{
			InstanceID: judged.InstanceID, Resolved: judged.Resolved,
			Reason: judged.Reason, BuildOK: judged.BuildOK,
			FailToPass: judged.FailToPass, PassToPass: judged.PassToPass,
			Metrics: report.Metrics{
				DurationMS: pred.Meta.DurationMS,
				ToolCalls:  pred.Meta.ToolCalls,
				DiffFiles:  pred.Meta.DiffFiles,
			},
		})
	}
	value.Tasks = results
	value.Totals = report.Summarize(results, preds)
	value.FinishedAt = time.Now().Format(time.RFC3339)
	report.PrintSummary(os.Stdout, value)
	reportPath := filepath.Join(*outDir, "report.json")
	if err = report.WriteReport(reportPath, value); err != nil {
		return err
	}
	fmt.Printf("报告写入 %s\n", reportPath)
	return nil
}

// runSmoke 用 gold 补丁验证判分器本身。
func runSmoke(args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	tasksDir := fs.String("tasks", "./tasks", "题库目录")
	fixtureDir := fs.String("fixture", "./fixture", "fixture 仓库目录")
	outDir := fs.String("out", "./results/smoke", "输出目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := task.Load(*tasksDir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*outDir, 0o755); err != nil { //nolint:gosec // 输出目录需可读执行
		return err
	}
	workRoot, err := os.MkdirTemp("", "euleval-smoke-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workRoot) }()
	value := report.Report{StartedAt: time.Now().Format(time.RFC3339)}
	results := make([]report.TaskResult, 0, len(items))
	preds := make([]report.Prediction, 0, len(items))
	for _, item := range items {
		data, readErr := os.ReadFile(item.GoldPatch())
		if readErr != nil {
			return readErr
		}
		pred := report.Prediction{
			InstanceID: item.ID, ModelNameOrPath: "gold",
			ModelPatch: string(data),
			Meta:       report.Meta{Status: "completed"},
		}
		preds = append(preds, pred)
		judged, judgeErr := judge.Run(item, pred.ModelPatch, judge.Options{
			FixtureDir: *fixtureDir, WorkRoot: workRoot,
		})
		if judgeErr != nil {
			return fmt.Errorf("smoke %s: %w", item.ID, judgeErr)
		}
		results = append(results, report.TaskResult{
			InstanceID: judged.InstanceID, Resolved: judged.Resolved,
			Reason: judged.Reason, BuildOK: judged.BuildOK,
			FailToPass: judged.FailToPass, PassToPass: judged.PassToPass,
		})
	}
	value.Tasks = results
	value.Totals = report.Summarize(results, preds)
	value.FinishedAt = time.Now().Format(time.RFC3339)
	report.PrintSummary(os.Stdout, value)
	if err = report.WriteReport(filepath.Join(*outDir, "report.json"), value); err != nil {
		return err
	}
	if value.Totals.Resolved != value.Totals.Tasks {
		failed := make([]string, 0, len(results))
		for _, result := range results {
			if !result.Resolved {
				failed = append(failed, result.InstanceID)
			}
		}
		return fmt.Errorf("smoke 未全部通过: %d/%d，失败题目: %s",
			value.Totals.Resolved, value.Totals.Tasks, strings.Join(failed, ", "))
	}
	return nil
}

// loadSelected 加载题库并按 --only 过滤。
func loadSelected(tasksDir, only string) ([]task.Task, error) {
	items, err := task.Load(tasksDir)
	if err != nil {
		return nil, err
	}
	return task.Filter(items, splitIDs(only))
}

// splitIDs 按逗号拆分 ID 列表，忽略空白项。
func splitIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
