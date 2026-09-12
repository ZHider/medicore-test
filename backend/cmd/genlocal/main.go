// Command genlocal 生成 configs/config.local.yaml，其中包含随机密钥。
//
// 仓库中不保存任何密钥，因此本地开发需要先执行本命令：
//
//	go run ./cmd/genlocal
//
// 生成的文件已被 .gitignore 排除，程序启动时会自动合并它（见 internal/config.Load）。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const keyBytes = 32

func main() {
	out := flag.String("out", filepath.Join("configs", "config.local.yaml"), "输出文件路径")
	force := flag.Bool("force", false, "覆盖已存在的文件")
	flag.Parse()

	// 默认拒绝覆盖：重新生成会换掉 AES 密钥，
	// 导致数据库中已加密的手机号/身份证永久无法解密，必须让人显式确认。
	if _, err := os.Stat(*out); err == nil && !*force {
		fmt.Fprintf(os.Stderr,
			"%s 已存在，未覆盖。\n\n"+
				"重新生成会换掉 aes_key，导致：\n"+
				"  - 数据库中已加密的手机号 / 身份证无法解密\n"+
				"  - phone_hash 不匹配，现有账号无法登录\n"+
				"  - 已签发的所有令牌失效\n\n"+
				"确认要重做请加 -force。\n", *out)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, []byte(render()), 0o600); err != nil {
		fatal(err)
	}

	fmt.Printf("已生成 %s\n", *out)
	fmt.Println("该文件已被 .gitignore 排除，不会被提交。")
}

func render() string {
	return fmt.Sprintf(`# 本地开发密钥，由 cmd/genlocal 生成。
# 本文件已被 .gitignore 排除，请勿提交。
#
# 注意：重新生成会换掉 aes_key，导致已有密文无法解密。
security:
  aes_key: "%s"        # 32 字节 hex，用于 AES-256-GCM
  phone_pepper: "%s"   # HMAC pepper，用于手机号/身份证摘要
  jwt_secret: "%s"     # JWT 签名密钥
`, randomHex(keyBytes), randomHex(keyBytes), randomHex(keyBytes))
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		fatal(err)
	}
	return hex.EncodeToString(b)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "错误: %v\n", err)
	os.Exit(1)
}
