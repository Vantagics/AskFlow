// Package email provides SMTP email sending functionality.
package email

import (
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"askflow/internal/config"
)

// Service sends emails via SMTP.
type Service struct {
	cfg func() config.SMTPConfig
}

// NewService creates an email service that reads SMTP config dynamically.
func NewService(cfgFn func() config.SMTPConfig) *Service {
	return &Service{cfg: cfgFn}
}

// SendVerification sends an email verification link to the user.
func (s *Service) SendVerification(toEmail, userName, verifyURL string) error {
	cfg := s.cfg()
	if cfg.Host == "" {
		return fmt.Errorf("SMTP 服务器未配置")
	}

	fromName := cfg.FromName
	if fromName == "" {
		fromName = "软件自助服务平台"
	}
	fromAddr := cfg.FromAddr
	if fromAddr == "" {
		fromAddr = cfg.Username
	}

	subject := "请验证您的邮箱"
	body := fmt.Sprintf(
		"您好 %s，\r\n\r\n"+
			"感谢您注册软件自助服务平台。\r\n\r\n"+
			"请点击以下链接验证您的邮箱：\r\n%s\r\n\r\n"+
			"该链接24小时内有效。\r\n\r\n"+
			"如果您没有注册过，请忽略此邮件。",
		userName, verifyURL,
	)

	msg := buildMessage(fromName, fromAddr, toEmail, subject, body)
	return s.send(cfg, fromAddr, toEmail, msg)
}
// SendTest sends a test email to verify SMTP configuration.
func (s *Service) SendTest(toEmail string) error {
	cfg := s.cfg()
	if cfg.Host == "" {
		return fmt.Errorf("SMTP 服务器未配置")
	}

	fromName := cfg.FromName
	if fromName == "" {
		fromName = "软件自助服务平台"
	}
	fromAddr := cfg.FromAddr
	if fromAddr == "" {
		fromAddr = cfg.Username
	}

	subject := "SMTP 测试邮件"
	body := "这是一封测试邮件，用于验证 SMTP 配置是否正确。\r\n\r\n如果您收到此邮件，说明邮件服务器配置正常。"

	msg := buildMessage(fromName, fromAddr, toEmail, subject, body)
	return s.send(cfg, fromAddr, toEmail, msg)
}

func buildMessage(fromName, fromAddr, to, subject, body string) []byte {
	// Sanitize headers to prevent email header injection
	sanitize := func(s string) string {
		s = strings.ReplaceAll(s, "\r", "")
		s = strings.ReplaceAll(s, "\n", "")
		s = strings.ReplaceAll(s, "\x00", "")
		return s
	}
	fromName = sanitize(fromName)
	fromAddr = sanitize(fromAddr)
	to = sanitize(to)
	subject = sanitize(subject)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("From: %s <%s>\r\n", fromName, fromAddr))
	sb.WriteString(fmt.Sprintf("To: %s\r\n", to))
	sb.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}

func (s *Service) send(cfg config.SMTPConfig, from, to string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	// Set a 15-second timeout for SMTP connection to prevent blocking
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return fmt.Errorf("连接邮件服务器失败: %w", err)
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer conn.Close()

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("创建SMTP客户端失败: %w", err)
	}
	defer client.Close()

	// Auth
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("邮件认证失败: %w", err)
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("发送邮件失败: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("发送邮件失败: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("发送邮件失败: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("发送邮件失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("发送邮件失败: %w", err)
	}
	return client.Quit()
}
