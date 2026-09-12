package mask

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPhone(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"标准手机号", "13812345678", "138****5678"},
		{"过短整体打码", "12345", "*****"},
		{"空串", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Phone(tt.input))
		})
	}
}

func TestIDCard(t *testing.T) {
	assert.Equal(t, "110101********1234", IDCard("110101199001011234"))
	assert.Equal(t, "*****", IDCard("12345"))
}

func TestName(t *testing.T) {
	assert.Equal(t, "张*", Name("张三"))
	assert.Equal(t, "欧**", Name("欧阳修"))
	assert.Equal(t, "李", Name("李"))
}

// 脱敏结果绝不能等于原文，否则脱敏形同虚设。
func TestMaskNeverEqualsOriginal(t *testing.T) {
	for _, s := range []string{"13812345678", "110101199001011234", "张三丰"} {
		assert.NotEqual(t, s, Phone(s))
		assert.NotEqual(t, s, IDCard(s))
		assert.NotEqual(t, s, Name(s))
	}
}
