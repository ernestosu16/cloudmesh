// Package mail envía correos por SMTP con la stdlib. Se configura con una
// URL: smtp://usuario:clave@host:587 (STARTTLS si el servidor lo ofrece) o
// smtps://usuario:clave@host:465 (TLS directo).
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// Sender envía correos a un servidor SMTP.
type Sender struct {
	addr     string // host:puerto
	host     string
	implicit bool // smtps: TLS desde el primer byte
	user     string
	pass     string
	from     mail.Address
}

// New interpreta la URL SMTP. Con rawURL vacía devuelve nil: no hay correo.
// from por defecto es el usuario de la URL.
func New(rawURL, from string) (*Sender, error) {
	if rawURL == "" {
		return nil, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "smtp" && u.Scheme != "smtps") {
		return nil, errors.New("CLOUDMESH_SMTP_URL debe ser smtp://usuario:clave@host:587 o smtps://usuario:clave@host:465")
	}
	s := &Sender{host: u.Hostname(), implicit: u.Scheme == "smtps", user: u.User.Username()}
	s.pass, _ = u.User.Password()
	port := u.Port()
	if port == "" {
		port = "587"
		if s.implicit {
			port = "465"
		}
	}
	s.addr = net.JoinHostPort(s.host, port)
	if from == "" {
		from = s.user
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("CLOUDMESH_SMTP_FROM inválido (%q): pon un remitente, p. ej. CloudMesh <cloudmesh@tudominio>", from)
	}
	s.from = *addr
	return s, nil
}

// Send manda un correo de texto plano en UTF-8.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("smtp: conectar: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if s.implicit {
		conn = tls.Client(conn, &tls.Config{ServerName: s.host})
	}
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer c.Close()
	if !s.implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.host}); err != nil {
				return fmt.Errorf("smtp: STARTTLS: %w", err)
			}
		}
	}
	if s.user != "" {
		// PlainAuth se niega a mandar la clave sin TLS salvo a localhost.
		if err := c.Auth(smtp.PlainAuth("", s.user, s.pass, s.host)); err != nil {
			return fmt.Errorf("smtp: autenticación: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("smtp: remitente: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp: destinatario: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if _, err := w.Write(message(s.from, to, subject, body)); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	return c.Quit()
}

func message(from mail.Address, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from.String() + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	// Líneas con CRLF; un «.» al principio lo escapa el escritor DATA de net/smtp.
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}
