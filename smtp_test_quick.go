// +build ignore

package main

import (
	"fmt"
	"log"

	"askflow/internal/config"
	"askflow/internal/email"
)

func main() {
	cfg := config.SMTPConfig{
		Host:       "smtp.189.cn",
		Port:       465,
		Username:   "13301168516@189.cn",
		Password:   "Ez$2Ib$9j*0Iv$3D",
		FromAddr:   "13301168516@189.cn",
		AuthMethod: "PLAIN",
	}

	svc := email.NewService(func() config.SMTPConfig { return cfg })
	err := svc.SendTest("znsoft@163.com")
	if err != nil {
		log.Fatalf("发送失败: %v", err)
	}
	fmt.Println("测试邮件发送成功！")
}
