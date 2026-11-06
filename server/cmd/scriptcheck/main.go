// Command scriptcheck validates one or more script.json files the way the
// server does on import. Used while growing the framework: every new field
// added here must keep old scripts loading and new ones strict.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"script-play/internal/domain"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法：scriptcheck <script.json> [更多文件…]")
		os.Exit(2)
	}
	bad := false
	for _, path := range os.Args[1:] {
		if err := check(path); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			bad = true
		}
	}
	if bad {
		os.Exit(1)
	}
}

func check(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s domain.Script
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	if errs := s.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  %v\n", e)
		}
		return fmt.Errorf("校验失败：%d 处问题", len(errs))
	}
	fmt.Printf("%s ok：%d 角色 / %d 段流程 / %d 条线索\n", path, len(s.Roles), len(s.Phases), len(s.Clues))
	return nil
}
