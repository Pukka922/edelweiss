package server

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// LimitListener returns a listener that hands out at most max concurrent
// connections per client IP address; further ones are closed at once, before
// a single byte is exchanged, and logged at most once a minute. A slot is free
// again when its connection is closed. Wrap it in TLS, not the other way round,
// so that clients are counted before their handshake.
//
// fasthttp's MaxConnsPerIP is not used: it writes its refusal from the accept
// loop, and on a TLS connection that write first waits for the client's
// handshake – a single silent client would stall every new connection. It also
// does not count IPv6 clients.
//
// Temporary accept errors (out of file descriptors, a connection aborted before
// it was accepted, timeouts) are retried with a pause growing from 5 ms to 1 s,
// like net/http does, and logged at most once a minute: fasthttp would take
// them for a dead listener and stop serving.
func LimitListener(ln net.Listener, max int, logger *slog.Logger) net.Listener {
	if logger == nil {
		logger = slog.Default()
	}
	return &limitListener{Listener: ln, max: max, logger: logger, conns: map[netip.Addr]int{}, done: make(chan struct{})}
}

type limitListener struct {
	net.Listener
	max    int
	logger *slog.Logger
	done   chan struct{} // closed by Close: ends a retry pause
	once   sync.Once

	mu            sync.Mutex
	conns         map[netip.Addr]int
	lastLogged    time.Time
	lastAcceptLog time.Time
}

func (l *limitListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

func (l *limitListener) Accept() (net.Conn, error) {
	var pause time.Duration
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || !temporaryAcceptError(err) {
				return nil, err
			}
			pause = min(max(2*pause, 5*time.Millisecond), time.Second)
			l.logAcceptError(err, pause)
			select {
			case <-time.After(pause):
				continue
			case <-l.done:
				return nil, net.ErrClosed
			}
		}
		pause = 0
		ip, ok := remoteIP(c)
		if !ok {
			return c, nil // not an IP connection: nothing to count
		}
		if l.admit(ip) {
			return &limitedConn{Conn: c, release: func() { l.release(ip) }}, nil
		}
		_ = c.Close()
	}
}

// temporaryAcceptError reports whether an accept error may go away by itself.
func temporaryAcceptError(err error) bool {
	if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ECONNABORTED) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var tmp interface{ Temporary() bool } // deprecated in net.Error, still how net/http decides
	return errors.As(err, &tmp) && tmp.Temporary()
}

func (l *limitListener) logAcceptError(err error, pause time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now := time.Now(); now.Sub(l.lastAcceptLog) >= time.Minute {
		l.lastAcceptLog = now
		l.logger.Warn("accepting connections failed, retrying", "err", err, "pause", pause)
	}
}

func (l *limitListener) admit(ip netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns[ip] >= l.max {
		if now := time.Now(); now.Sub(l.lastLogged) >= time.Minute {
			l.lastLogged = now
			l.logger.Warn("refusing connections: too many from one address", "ip", ip.String(), "max", l.max)
		}
		return false
	}
	l.conns[ip]++
	return true
}

func (l *limitListener) release(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns[ip]--; l.conns[ip] <= 0 {
		delete(l.conns, ip)
	}
}

func remoteIP(c net.Conn) (netip.Addr, bool) {
	tcp, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	ip := tcp.AddrPort().Addr().Unmap().WithZone("")
	return ip, ip.IsValid()
}

// limitedConn gives its slot back when it is closed, once.
type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
