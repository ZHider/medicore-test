package prompts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreconsultSystemPrompt(t *testing.T) {
	t.Run("返回 v1 内容", func(t *testing.T) {
		got, err := PreconsultSystemPrompt(PreconsultV1)
		require.NoError(t, err)
		assert.NotEmpty(t, got)
		// 提示词必须包含最关键的禁令，否则模型会越界
		assert.Contains(t, got, "绝不下诊断")
		assert.Contains(t, got, "绝不开药")
	})

	t.Run("空版本使用当前推荐版本", func(t *testing.T) {
		explicit, err := PreconsultSystemPrompt(PreconsultLatest)
		require.NoError(t, err)

		implicit, err := PreconsultSystemPrompt("")
		require.NoError(t, err)

		assert.Equal(t, explicit, implicit)
	})

	// 报错必须列出可用版本，否则运维只能去翻文件
	t.Run("未知版本报错并列出可用版本", func(t *testing.T) {
		_, err := PreconsultSystemPrompt("v99")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "v99")
		assert.Contains(t, err.Error(), PreconsultV1)
	})
}

func TestPreconsultSummaryPrompt(t *testing.T) {
	got, err := PreconsultSummaryPrompt(PreconsultV1)
	require.NoError(t, err)
	assert.NotEmpty(t, got)
	// 总结提示词的核心约束：只依据对话、不下诊断、只输出 JSON
	assert.Contains(t, got, "只输出一个 JSON 对象")
	assert.Contains(t, got, "不下诊断")
}

// Schema 必须是可解析的 JSON，且结构自洽。
func TestPreconsultSummarySchema(t *testing.T) {
	raw, err := PreconsultSummarySchema(PreconsultV1)
	require.NoError(t, err)

	var schema struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &schema), "Schema 必须是合法 JSON")

	assert.Equal(t, "object", schema.Type)
	assert.NotEmpty(t, schema.Required, "必须有必填字段，否则校验形同虚设")

	// 每个必填字段都要在 properties 里有定义，否则 Schema 自身是矛盾的
	for _, field := range schema.Required {
		_, defined := schema.Properties[field]
		assert.True(t, defined, "必填字段 %q 未在 properties 中定义", field)
	}
}

// 关键一致性检查：三个文件必须同时存在。
//
// 若只给某个版本加了 chat prompt 却忘了 summary prompt 或 schema，
// 程序会在启动时才失败，而那时已经打包完毕。这个用例把它提前到测试阶段。
func TestPreconsultArtifactsAreVersionConsistent(t *testing.T) {
	versions := PreconsultVersions()
	require.Contains(t, versions, PreconsultLatest, "推荐版本必须在可用列表中")

	for _, v := range versions {
		t.Run(v, func(t *testing.T) {
			_, err := PreconsultSystemPrompt(v)
			assert.NoError(t, err, "缺少 %s 的对话提示词", v)

			_, err = PreconsultSummaryPrompt(v)
			assert.NoError(t, err, "缺少 %s 的总结提示词", v)

			_, err = PreconsultSummarySchema(v)
			assert.NoError(t, err, "缺少 %s 的总结 Schema", v)
		})
	}
}

func TestPreconsultVersions(t *testing.T) {
	versions := PreconsultVersions()

	assert.NotEmpty(t, versions)
	assert.Contains(t, versions, "v1")
	// 版本列表应去重且有序
	assert.Equal(t, versions, sortedUnique(versions))
}

func TestReadVersioned_UnknownExtension(t *testing.T) {
	_, err := readVersioned("preconsult", "txt", PreconsultV1)
	assert.Error(t, err)
}

func sortedUnique(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	// 简单插入排序：版本数量极少，够用且无额外依赖
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && strings.Compare(out[j], out[j-1]) < 0; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
