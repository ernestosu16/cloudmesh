package mail

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

// fakeSMTP habla lo justo del protocolo y devuelve lo recibido en DATA.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		say("220 fake")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					got <- data.String()
					say("250 ok")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "DATA"):
				inData = true
				say("354 adelante")
			case strings.HasPrefix(cmd, "QUIT"):
				say("221 adiós")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSend(t *testing.T) {
	addr, got := fakeSMTP(t)
	s, err := New("smtp://"+addr, "CloudMesh <cm@ejemplo.test>")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), "ana@ejemplo.test", "Invitación a CloudMesh", "Hola:\n\nabre el enlace\n"); err != nil {
		t.Fatal(err)
	}
	msg := <-got
	for _, want := range []string{"From: \"CloudMesh\" <cm@ejemplo.test>", "To: ana@ejemplo.test", "Subject: =?utf-8?q?", "charset=utf-8", "abre el enlace\r\n"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("falta %q en:\n%s", want, msg)
		}
	}
}

func TestNew(t *testing.T) {
	if s, err := New("", ""); s != nil || err != nil {
		t.Fatalf("sin URL = %v %v", s, err)
	}
	for _, bad := range []string{"http://x", "smtp://", "smtp://u@host"} {
		if _, err := New(bad, ""); err == nil && bad != "smtp://u@host" {
			t.Errorf("%s: esperaba error", bad)
		}
	}
	if _, err := New("smtp://host", ""); err == nil {
		t.Error("sin remitente ni usuario: esperaba error")
	}
	s, err := New("smtps://user%40ejemplo.test:clave@mail.ejemplo.test", "")
	if err != nil || s.addr != "mail.ejemplo.test:465" || !s.implicit || s.from.Address != "user@ejemplo.test" {
		t.Fatalf("smtps = %+v %v", s, err)
	}
}
