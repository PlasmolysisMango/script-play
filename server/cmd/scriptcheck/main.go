// Command scriptcheck validates one or more script.json files the way the
// server does on import. Used while growing the framework: every new field
// added here must keep old scripts loading and new ones strict.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"script-play/internal/domain"
)

// Pictures live in free text, so both patterns are matched against the whole
// marshalled script: a field added later is covered without touching this list.
var (
	scriptAssetRef = regexp.MustCompile(`/script-assets/[^)"'\s]+`)
	legacyStoryRef = regexp.MustCompile(`/story/[^)"'\s]+`)
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
	n, err := checkAssets(path, &s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %v\n", err)
		return fmt.Errorf("图片检查失败")
	}
	fmt.Printf("%s ok：%d 角色 / %d 段流程 / %d 条线索 / %d 张配图引用\n", path, len(s.Roles), len(s.Phases), len(s.Clues), n)
	return nil
}

// checkAssets resolves every /script-assets/... reference against the pack on
// disk. A missing picture is a broken page for the player, and a /story/ link
// is a leftover from the retired global image directory — both fail the gate.
func checkAssets(path string, s *domain.Script) (int, error) {
	root := filepath.Dir(filepath.Dir(path))
	blob, err := json.Marshal(s)
	if err != nil {
		return 0, err
	}
	body := string(blob)
	if legacy := legacyStoryRef.FindString(body); legacy != "" {
		return 0, fmt.Errorf("还在用已废弃的全局图目录：%s", legacy)
	}
	seen := map[string]bool{}
	var missing []string
	for _, ref := range scriptAssetRef.FindAllString(body, -1) {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		// A body may carry a cache-busting query (`?v=2`) so a re-cropped
		// picture reaches browsers that already hold the old one; the file
		// on disk is named without it and the handler ignores it too.
		rel := strings.TrimPrefix(ref, "/script-assets/")
		if i := strings.IndexAny(rel, "?#"); i >= 0 {
			rel = rel[:i]
		}
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil || st.IsDir() {
			missing = append(missing, ref)
		}
	}
	if len(missing) > 0 {
		return 0, fmt.Errorf("配图找不到 %d 处：%s", len(missing), strings.Join(missing, "，"))
	}
	return len(seen), nil
}
