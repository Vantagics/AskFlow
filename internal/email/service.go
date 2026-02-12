// Package email provides SMTP email sending functionality.
package email

import (
	"fmt"
	"net/smtp"
	"strings"

	"helpdesk/internal/config"
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
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if err := smtp.SendMail(addr, auth, from, []string{to}, msg); err != nil {
		return fmt.Errorf("发送邮件失败: %w", err)
	}
	return nil
}
