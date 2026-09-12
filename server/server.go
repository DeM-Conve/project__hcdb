package server

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/hchauhan7816/hcdb/db"
	"github.com/hchauhan7816/hcdb/resp"
)

// Server accepts one connection per goroutine and dispatches each parsed
// RESP command against one shared *db.DB. The DB is opened once by the
// caller and handed in — every connection operates on the same database,
// same as every other caller of the db package.
type Server struct {
	database *db.DB
	listener atomic.Pointer[net.Listener]

	wg sync.WaitGroup

	connsMu sync.Mutex
	conns   map[net.Conn]struct{} // live connections, so shutdown can close idle ones
}

func New(database *db.DB) *Server {
	return &Server{database: database, conns: make(map[net.Conn]struct{})}
}

// Serve listens on addr and accepts connections until ctx is cancelled.
// Blocks until the listener and every connection goroutine have exited.
//
// A connection idle between commands is blocked inside resp.Read, which has
// no ctx-aware variant — closing the listener alone would stop new accepts
// but leave that goroutine waiting forever. So shutdown closes every live
// connection too, which unblocks the read with an error and lets the
// goroutine return.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener.Store(&ln)

	go func() {
		<-ctx.Done()
		ln.Close()
		s.closeAllConns()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				s.wg.Wait()
				return nil
			}
			return err
		}

		s.addConn(conn)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.removeConn(conn)
			s.handleConn(conn)
		}()
	}
}

func (s *Server) addConn(conn net.Conn) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	s.conns[conn] = struct{}{}
}

func (s *Server) removeConn(conn net.Conn) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	delete(s.conns, conn)
}

func (s *Server) closeAllConns() {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	for conn := range s.conns {
		conn.Close()
	}
}

// handleConn serves one connection until the client disconnects, the server
// shuts down, or the parser hits something it can't recover from. One
// goroutine per connection — the Go runtime's netpoller parks it off its OS
// thread while conn.Read is waiting on the socket, so this scales past what
// one goroutine per OS thread would allow.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	for {
		cmd, err := resp.Read(r)
		if err != nil {
			return
		}

		reply := dispatch(s.database, cmd)

		if err := resp.Write(w, reply); err != nil {
			return
		}
	}
}

// Addr returns the listener's bound address, or nil if Serve hasn't started
// listening yet. Mainly useful in tests that bind to ":0" and need the
// actual port picked by the OS.
func (s *Server) Addr() net.Addr {
	ln := s.listener.Load()
	if ln == nil {
		return nil
	}
	return (*ln).Addr()
}
