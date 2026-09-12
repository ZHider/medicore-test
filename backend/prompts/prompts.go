// Package prompts 承载版本化的提示词模板与输出 Schema。
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

//go:embed *.md *.json
var files embed.FS

// 预问诊相关提示词的版本常量。
const (
	// PreconsultV1 对应 preconsult_v1.md / preconsult_summary_v1.*。
	PreconsultV1 = "v1"
	// PreconsultLatest 指向当前推荐的版本。
	PreconsultLatest = PreconsultV1
)

// PreconsultSystemPrompt 返回指定版本的对话 system prompt。
// version 传空串时使用当前推荐版本。
func PreconsultSystemPrompt(version string) (string, error) {
	return readVersioned("preconsult", "md", version)
}

// PreconsultSummaryPrompt 返回指定版本的总结 system prompt。
func PreconsultSummaryPrompt(version string) (string, error) {
	return readVersioned("preconsult_summary", "md", version)
}

// PreconsultSummarySchema 返回指定版本的结构化输出 Schema（JSON 文本）。
//
// Schema 与 prompt 同版本号管理：调整了字段就必须同时改两者，
// 共用版本号能让"prompt 与校验规则不匹配"这种问题在追溯时一眼可见。
func PreconsultSummarySchema(version string) (string, error) {
	return readVersioned("preconsult_summary", "json", version)
}

// PreconsultVersions 返回全部可用的预问诊 prompt 版本。
// 供启动时自检与运维排查使用。
func PreconsultVersions() []string {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "preconsult_") {
			continue
		}
		ext := path.Ext(name)
		if ext != ".md" && ext != ".json" {
			continue
		}
		base := strings.TrimSuffix(name, ext)
		// preconsult_summary_v1 -> v1，preconsult_v1 -> v1
		idx := strings.LastIndex(base, "_")
		if idx < 0 {
			continue
		}
		seen[base[idx+1:]] = struct{}{}
	}

	versions := make([]string, 0, len(seen))
	for v := range seen {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	return versions
}

func readVersioned(base, ext, version string) (string, error) {
	if version == "" {
		version = PreconsultLatest
	}

	name := fmt.Sprintf("%s_%s.%s", base, version, ext)
	raw, err := files.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("prompt %s 不存在（可用版本：%s）: %w",
			name, strings.Join(PreconsultVersions(), ", "), err)
	}
	return string(raw), nil
}
