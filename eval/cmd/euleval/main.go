// Command euleval 是 Euler 编码能力评测工具的入口。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"euler/eval/internal/task"
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
