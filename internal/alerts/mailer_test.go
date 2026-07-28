package alerts

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeSMTPServer speaks just enough RFC 5321 for one message and records the
// envelope and data it received. No STARTTLS is offered, so the client sends
// in the clear — which is exactly the code path under test.
type fakeSMTPServer struct {
	addr string
	got  chan smtpDelivery
}

type smtpDelivery struct {
	from  string
	rcpts []string
	data  string
}

func startFakeSMTP(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &fakeSMTPServer{addr: ln.Addr().String(), got: make(chan smtpDelivery, 1)}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReader(conn)
		say := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
		say("220 fake ESMTP")
		var d smtpDelivery
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250-fake")
				say("250 OK")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				d.from = cmd
				say("250 OK")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				d.rcpts = append(d.rcpts, cmd)
				say("250 OK")
			case cmd == "DATA":
				say("354 go ahead")
				var b strings.Builder
				for {
					dl, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(dl, "\r\n") == "." {
						break
					}
					b.WriteString(dl)
				}
				d.data = b.String()
				say("250 accepted")
			case cmd == "QUIT":
				say("221 bye")
				s.got <- d
				return
			default:
				say("250 OK")
			}
		}
	}()
	return s
}

func TestSMTPMailerDeliversMessage(t *testing.T) {
	srv := startFakeSMTP(t)
	host, portStr, _ := net.SplitHostPort(srv.addr)
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}

	cfg := testConfig()
	cfg.SMTPHost = host
	cfg.SMTPPort = port
	cfg.From = "owlwatch@example.com"
	cfg.To = []string{"ops@example.com", "oncall@example.com"}
	m := &smtpMailer{cfg: cfg}
	if err := m.Send("[owlwatch] web1: test", "hello\r\n"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case d := <-srv.got:
		if !strings.Contains(d.from, "owlwatch@example.com") {
			t.Fatalf("MAIL FROM = %q", d.from)
		}
		if len(d.rcpts) != 2 || !strings.Contains(d.rcpts[1], "oncall@example.com") {
			t.Fatalf("RCPT TO = %v, want both recipients", d.rcpts)
		}
		for _, want := range []string{
			"Subject: [owlwatch] web1: test",
			"From: owlwatch@example.com",
			"To: ops@example.com, oncall@example.com",
			"Content-Type: text/plain; charset=utf-8",
			"hello",
		} {
			if !strings.Contains(d.data, want) {
				t.Fatalf("message missing %q:\n%s", want, d.data)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("server never received the message")
	}
}
