// Package email provides SMTP email sending functionality.
package email

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"askflow/internal/config"
)

// loginAuth implements smtp.Auth for the LOGIN mechanism.
// Go's standard library only provides PlainAuth, but many mail servers
// (especially Chinese providers like 189, QQ, 163) require or only support LOGIN.
type loginAuth struct {
	username, password string
}

func newLoginAuth(username, password string) smtp.Auth {
	return &loginAuth{username, password}
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", []byte(a.username), nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		prompt := strings.TrimSpace(string(fromServer))
		switch strings.ToLower(prompt) {
		case "username:", "user name", "user name:":
			return []byte(a.username), nil
		case "password:", "password":
			return []byte(a.password), nil
		default:
			return nil, fmt.Errorf("unexpected LOGIN prompt: %s", prompt)
		}
	}
	return nil, nil
}

// unrestrictedPlainAuth implements smtp.Auth for PLAIN without the TLS check.
// Go's smtp.PlainAuth refuses to send credentials over unencrypted connections
// and sometimes fails to detect TLS on implicit-TLS (port 465) connections.
// This implementation skips that check since we manage TLS ourselves.
type unrestrictedPlainAuth struct {
	identity, username, password, host string
}

func newUnrestrictedPlainAuth(identity, username, password, host string) smtp.Auth {
	return &unrestrictedPlainAuth{identity, username, password, host}
}

func (a *unrestrictedPlainAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	resp := []byte(a.identity + "\x00" + a.username + "\x00" + a.password)
	return "PLAIN", resp, nil
}

func (a *unrestrictedPlainAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge during PLAIN auth")
	}
	return nil, nil
}

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

	var conn net.Conn
	var err error

	// Port 465 uses implicit TLS (SMTPS), need to establish TLS connection first
	// Port 587/25 use STARTTLS (explicit TLS) after plain connection
	if cfg.Port == 465 {
		// Implicit TLS: connect with TLS directly
		dialer := &net.Dialer{Timeout: 15 * time.Second}
		tlsConfig := &tls.Config{
			ServerName: cfg.Host,
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("TLS连接邮件服务器失败: %w", err)
		}
	} else {
		// Plain connection (for STARTTLS on port 587/25)
		conn, err = net.DialTimeout("tcp", addr, 15*time.Second)
		if err != nil {
			return fmt.Errorf("连接邮件服务器失败: %w", err)
		}
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer conn.Close()

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("创建SMTP客户端失败: %w", err)
	}
	defer client.Close()

	// For non-465 ports, try STARTTLS if available
	if cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			tlsConfig := &tls.Config{ServerName: cfg.Host}
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("STARTTLS失败: %w", err)
			}
		}
	}

	// Auth
	var auth smtp.Auth
	method := strings.ToUpper(strings.TrimSpace(cfg.AuthMethod))
	switch method {
	case "LOGIN":
		auth = newLoginAuth(cfg.Username, cfg.Password)
	case "NONE", "NOAUTH":
		// Skip authentication entirely (for relay servers)
		auth = nil
	default:
		// Default to PLAIN with our unrestricted implementation
		// that works correctly on implicit TLS (port 465) connections
		auth = newUnrestrictedPlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("邮件认证失败 (auth=%s): %w", method, err)
		}
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
