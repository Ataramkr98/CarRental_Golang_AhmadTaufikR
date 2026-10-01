package services

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
)

// smtpConfigured reports whether an outbound mail server is set. When it is
// not, password-reset links are returned to the caller instead of e-mailed —
// the same demo/live split the payment gateway uses.
func smtpConfigured() bool {
	return strings.TrimSpace(os.Getenv("SMTP_HOST")) != ""
}

// smtpAddress builds the host:port dial target for the SMTP server.
func smtpAddress() string {
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	port := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	if port == "" {
		port = "587"
	}
	if _, err := strconv.Atoi(port); err != nil {
		port = "587"
	}
	return net.JoinHostPort(host, port)
}

// mailFrom resolves the envelope sender, falling back to the SMTP username.
func mailFrom() string {
	if value := strings.TrimSpace(os.Getenv("MAIL_FROM")); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("SMTP_USERNAME"))
}

// sendPasswordResetEmail delivers the reset link through the configured SMTP
// server. Plain authentication is used; net/smtp negotiates STARTTLS when the
// server offers it, which every mainstream provider does.
func sendPasswordResetEmail(to, resetURL string) error {
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	from := mailFrom()
	if from == "" {
		return fmt.Errorf("MAIL_FROM or SMTP_USERNAME must be set to send mail")
	}

	subject := "Reset your DriveNow password"
	body := strings.Join([]string{
		"Someone asked to reset the password for this DriveNow account.",
		"",
		"Open the link below within 30 minutes to choose a new password:",
		resetURL,
		"",
		"If you did not request this, you can safely ignore this e-mail.",
	}, "\r\n")

	// RFC 5322 headers; subject is plain ASCII by policy (set it accordingly).
	message := []byte(strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n"))

	var auth smtp.Auth
	if username := strings.TrimSpace(os.Getenv("SMTP_USERNAME")); username != "" {
		auth = smtp.PlainAuth("", username, os.Getenv("SMTP_PASSWORD"), host)
	}

	// Prefer implicit-TLS ports (465) when configured; otherwise the plain
	// connection with STARTTLS (587) that net/smtp handles automatically.
	if strings.TrimSpace(os.Getenv("SMTP_PORT")) == "465" {
		return sendMailTLS(smtpAddress(), auth, from, []string{to}, message)
	}
	return smtp.SendMail(smtpAddress(), auth, from, []string{to}, message)
}

// sendMailTLS dials SMTPS (implicit TLS, port 465) and issues STARTTLS once
// the greeting completes, then sends the message.
func sendMailTLS(addr string, auth smtp.Auth, from string, to []string, message []byte) error {
	connection, err := tls.Dial("tcp", addr, &tls.Config{ServerName: strings.Split(addr, ":")[0]})
	if err != nil {
		return err
	}
	defer connection.Close()

	client, err := smtp.NewClient(connection, strings.Split(addr, ":")[0])
	if err != nil {
		return err
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
