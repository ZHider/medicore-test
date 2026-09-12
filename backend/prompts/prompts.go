// Package prompts 承载版本化的提示词模板。
//
// 用 go:embed 而非运行时读文件：prompt 是代码的一部分，
// 编进二进制后不会出现"部署时漏拷 prompts 目录"这类环境差异问题，
// 版本号也随之与代码版本绑定，便于追溯（spec §7.2 要求版本入库）。
package prompts

import (
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// 预问诊相关提示词的版本常量。
const (
	// PreconsultV1 对应 preconsult_v1.md。
	PreconsultV1 = "v1"
	// PreconsultLatest 指向当前推荐的版本。
	PreconsultLatest = PreconsultV1
)

// PreconsultSystemPrompt 返回指定版本的预问诊 system prompt。
// version 传空串时使用当前推荐版本。
func PreconsultSystemPrompt(version string) (string, error) {
	if version == "" {
		version = PreconsultLatest
	}

	name := fmt.Sprintf("preconsult_%s.md", version)
	raw, err := files.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("prompt %s 不存在（可用版本：%s）: %w", name, strings.Join(PreconsultVersions(), ", "), err)
	}
	return string(raw), nil
}

// PreconsultVersions 返回全部可用的预问诊 prompt 版本。
// 供启动时自检与运维排查使用。
func PreconsultVersions() []string {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil
	}

	var versions []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "preconsult_") || path.Ext(name) != ".md" {
			continue
		}
		versions = append(versions, strings.TrimSuffix(strings.TrimPrefix(name, "preconsult_"), ".md"))
	}
	sort.Strings(versions)
	return versions
}
