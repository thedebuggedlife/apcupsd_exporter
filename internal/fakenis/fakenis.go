// Package fakenis is a fake apcupsd NIS that speaks the real wire protocol, for tests and manual runs.
package fakenis

import (
	"embed"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
)

//go:embed responses/*.txt
var responses embed.FS

// Names returns the sorted names of the canned responses.
func Names() []string {
	entries, err := responses.ReadDir("responses")
	if err != nil {
		panic(err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".txt"))
	}
	sort.Strings(names)

	return names
}

// Response returns the named canned response as one message per line, newlines kept as apcupsd sends them.
func Response(name string) ([]string, error) {
	b, err := responses.ReadFile(path.Join("responses", name+".txt"))
	if err != nil {
		return nil, fmt.Errorf("unknown response %q, want one of: %s", name, strings.Join(Names(), ", "))
	}

	var messages []string
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if strings.TrimSpace(line) != "" {
			messages = append(messages, line)
		}
	}

	return messages, nil
}

// A Server answers each "status" command with a fixed list of messages.
type Server struct {
	l        net.Listener
	response []byte
	wg       sync.WaitGroup

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

// Listen starts a Server on the TCP address addr. Each message must be 1 to 65535 bytes.
func Listen(addr string, messages []string) (*Server, error) {
	response, err := encode(messages)
	if err != nil {
		return nil, err
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	s := &Server{
		l:        l,
		response: response,
		conns:    make(map[net.Conn]struct{}),
	}

	s.wg.Add(1)
	go s.accept()

	return s, nil
}

// Addr returns the address the Server is listening on.
func (s *Server) Addr() string { return s.l.Addr().String() }

// Close stops the Server, closes its connections and waits for them to finish.
func (s *Server) Close() error {
	err := s.l.Close()

	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()

	return err
}

func (s *Server) accept() {
	defer s.wg.Done()

	for {
		c, err := s.l.Accept()
		if err != nil {
			return
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()

		go s.serve(c)
	}
}

func (s *Server) serve(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.Close()
	}()

	for {
		cmd, err := readMessage(c)
		if err != nil || cmd != "status" {
			return
		}
		if _, err := c.Write(s.response); err != nil {
			return
		}
	}
}

func readMessage(r io.Reader) (string, error) {
	var prefix [2]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return "", err
	}

	b := make([]byte, binary.BigEndian.Uint16(prefix[:]))
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}

	return string(b), nil
}

func encode(messages []string) ([]byte, error) {
	var b []byte
	for _, m := range messages {
		if m == "" {
			return nil, fmt.Errorf("empty message would end the response early")
		}
		if len(m) > math.MaxUint16 {
			return nil, fmt.Errorf("%d byte message does not fit a 2-byte length prefix", len(m))
		}

		var prefix [2]byte
		binary.BigEndian.PutUint16(prefix[:], uint16(len(m)))
		b = append(b, prefix[:]...)
		b = append(b, m...)
	}

	return append(b, 0, 0), nil
}
