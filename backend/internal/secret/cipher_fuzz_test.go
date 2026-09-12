package secret

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 加解密是"外部可控字节 → 内部逻辑"的入口。
//
// Decrypt 的输入来自数据库，而数据库里的密文可能因为任何原因损坏
// （截断、手工改过、密钥轮换后残留）。这里要保证的性质只有两条：
//
//  1. 绝不 panic —— panic 在 HTTP handler 里会被 Recovery 拦成 500，
//     但如果是后台任务（worker / 定时任务）里 panic，整个进程就没了。
//  2. 要么返回原文，要么返回错误，绝不返回"看起来正常但是错的"结果。
//
// 用 fuzz 而不是表驱动：损坏密文的形态是无穷的，手工列举一定会漏。
func FuzzDecrypt(f *testing.F) {
	cipher, err := NewCipher(strings.Repeat("ab", 32))
	if err != nil {
		f.Fatal(err)
	}

	// 种子：正常密文、空、截断、纯随机、合法前缀但篡改的
	valid, err := cipher.Encrypt("13800001111")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add(valid[:5])
	f.Add(valid[:len(valid)-1])
	f.Add(make([]byte, 12))
	f.Add([]byte("not-a-ciphertext-at-all"))

	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := cipher.Decrypt(data)
		if err != nil {
			return // 绝大多数输入都会走到这里，正是预期
		}

		// 只有解密成功时才检查：解出来的必须是合法 UTF-8 明文。
		// 若 GCM 校验通过却解出非法字节，说明 nonce/密文切分错了位。
		if !utf8.ValidString(got) {
			t.Fatalf("解密成功却得到非法 UTF-8：%q（原始字节 % x）", got, data)
		}
	})
}

// 往返性质：任意明文字符串都必须能原样取回。
//
// 这条比"某个具体号码能解开"强得多 —— 它覆盖了长度、编码、边界字节，
// 而这些正是 AES-GCM 分块与 nonce 拼接最容易出错的地方。
func FuzzEncryptDecryptRoundTrip(f *testing.F) {
	cipher, err := NewCipher(strings.Repeat("cd", 32))
	if err != nil {
		f.Fatal(err)
	}

	f.Add("")
	f.Add("13800001111")
	f.Add("张三")
	f.Add(strings.Repeat("长", 1000))
	f.Add("\x00\x01\x02") // 含 NUL 与二进制控制字符

	f.Fuzz(func(t *testing.T, plaintext string) {
		enc, err := cipher.Encrypt(plaintext)
		if err != nil {
			t.Skip() // 超长明文被拒是合法的
		}

		got, err := cipher.Decrypt(enc)
		if err != nil {
			t.Fatalf("自己加密的数据解不开：%v（明文 %q）", err, plaintext)
		}
		if got != plaintext {
			t.Fatalf("往返不一致：want %q, got %q", plaintext, got)
		}

		// 同一明文两次加密必须产生不同密文（随机 nonce）
		enc2, err := cipher.Encrypt(plaintext)
		if err != nil {
			t.Fatal(err)
		}
		if string(enc) == string(enc2) && plaintext != "" {
			t.Fatal("两次加密产生相同密文：nonce 没有随机化，等于把明文暴露成可比对")
		}
	})
}
