package fakenis_test

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mdlayher/apcupsd"
	"github.com/mdlayher/apcupsd_exporter/internal/fakenis"
)

const statusRequest = "\x00\x06status"

func TestRealClientReadsCannedResponses(t *testing.T) {
	tests := []struct {
		response string
		wantAmps float64
	}{
		{response: "full", wantAmps: 0.96},
		{response: "no-outcurnt", wantAmps: 0},
	}

	for _, tt := range tests {
		t.Run(tt.response, func(t *testing.T) {
			messages, err := fakenis.Response(tt.response)
			if err != nil {
				t.Fatalf("failed to load response: %v", err)
			}
			srv := listen(t, messages)

			c, err := apcupsd.Dial("tcp", srv.Addr())
			if err != nil {
				t.Fatalf("failed to dial: %v", err)
			}
			defer c.Close()

			// A real NIS keeps the connection open between commands.
			for i := 0; i < 2; i++ {
				s, err := c.Status()
				if err != nil {
					t.Fatalf("status %d failed: %v", i, err)
				}
				if s.UPSName != "example-ups" || s.Hostname != "example-host" || s.Status != "ONLINE" {
					t.Errorf("unexpected status %d: %+v", i, s)
				}
				if s.OutputAmps != tt.wantAmps {
					t.Errorf("unexpected output amps: got %v, want %v", s.OutputAmps, tt.wantAmps)
				}
			}
		})
	}
}

func TestWireFormat(t *testing.T) {
	srv := listen(t, []string{"A : 1\n", "BC : 2\n"})
	conn := dial(t, srv)

	if _, err := io.WriteString(conn, statusRequest); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	const want = "\x00\x06A : 1\n\x00\x07BC : 2\n\x00\x00"
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if string(got) != want {
		t.Errorf("unexpected bytes: got %q, want %q", got, want)
	}
}

func TestUnknownCommandClosesConnection(t *testing.T) {
	srv := listen(t, []string{"A : 1\n"})
	conn := dial(t, srv)

	if _, err := io.WriteString(conn, "\x00\x06events"); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	n, err := conn.Read(make([]byte, 1))
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("unexpected read result: n=%d, err=%v", n, err)
	}
}

func TestOversizedResponseArrivesWhole(t *testing.T) {
	messages, err := fakenis.Response("oversized")
	if err != nil {
		t.Fatalf("failed to load response: %v", err)
	}
	srv := listen(t, messages)
	conn := dial(t, srv)

	if _, err := io.WriteString(conn, statusRequest); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	// The client's read buffer holds 256 bytes, so the message must exceed it.
	longest := 0
	for {
		var prefix [2]byte
		if _, err := io.ReadFull(conn, prefix[:]); err != nil {
			t.Fatalf("failed to read length prefix: %v", err)
		}
		length := int(binary.BigEndian.Uint16(prefix[:]))
		if length == 0 {
			break
		}
		if _, err := io.ReadFull(conn, make([]byte, length)); err != nil {
			t.Fatalf("failed to read %d byte message: %v", length, err)
		}
		if length > longest {
			longest = length
		}
	}

	if longest <= 256 {
		t.Errorf("longest message is %d bytes, want more than 256", longest)
	}
}

func TestServesClientsConcurrently(t *testing.T) {
	messages, err := fakenis.Response("full")
	if err != nil {
		t.Fatalf("failed to load response: %v", err)
	}
	srv := listen(t, messages)

	// This connection never sends a command, so it stays in the read loop.
	dial(t, srv)

	conn := dial(t, srv)
	c := apcupsd.New(conn)
	if _, err := c.Status(); err != nil {
		t.Fatalf("second client failed: %v", err)
	}
}

func TestCloseReturnsWithIdleClient(t *testing.T) {
	srv, err := fakenis.Listen("127.0.0.1:0", []string{"A : 1\n"})
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	conn := dial(t, srv)

	// A write proves the server accepted the connection before Close runs.
	if _, err := io.WriteString(conn, statusRequest); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 2+len("A : 1\n")+2)); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- srv.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("close failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
}

func TestListenRejectsUnframeableMessages(t *testing.T) {
	tests := []struct {
		desc    string
		message string
	}{
		{desc: "empty would end the response", message: ""},
		{desc: "longer than the length prefix", message: strings.Repeat("x", 65536)},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			srv, err := fakenis.Listen("127.0.0.1:0", []string{tt.message})
			if err == nil {
				srv.Close()
				t.Fatal("expected an error")
			}
		})
	}
}

func TestListenAcceptsLongestFrameableMessage(t *testing.T) {
	message := strings.Repeat("x", 65535)
	srv := listen(t, []string{message})
	conn := dial(t, srv)

	if _, err := io.WriteString(conn, statusRequest); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	got := make([]byte, 2+len(message)+2)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}
	if got[0] != 0xff || got[1] != 0xff {
		t.Errorf("unexpected length prefix: %x", got[:2])
	}
}

func TestEveryListedResponseLoads(t *testing.T) {
	names := fakenis.Names()
	if len(names) == 0 {
		t.Fatal("no canned responses")
	}

	for _, name := range names {
		messages, err := fakenis.Response(name)
		if err != nil {
			t.Errorf("failed to load %q: %v", name, err)
		}
		if len(messages) == 0 {
			t.Errorf("response %q has no messages", name)
		}
	}
}

func TestResponseUnknownNameListsChoices(t *testing.T) {
	_, err := fakenis.Response("nonexistent")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no-outcurnt") {
		t.Errorf("error does not list the choices: %v", err)
	}
}

func TestResponsesDropBlankLinesAndKeepNewlines(t *testing.T) {
	messages, err := fakenis.Response("no-outcurnt")
	if err != nil {
		t.Fatalf("failed to load response: %v", err)
	}

	for i, m := range messages {
		if strings.TrimSpace(m) == "" || !strings.HasSuffix(m, "\n") {
			t.Errorf("message %d: got %q, want a non-blank line ending in a newline", i, m)
		}
	}
}

func listen(t *testing.T, messages []string) *fakenis.Server {
	t.Helper()

	srv, err := fakenis.Listen("127.0.0.1:0", messages)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	return srv
}

func dial(t *testing.T, srv *fakenis.Server) net.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}

	return conn
}
