package compose

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

type (
	// deadlineConn wraps net.Conn to apply SetReadDeadline before each Read call.
	deadlineConn struct {
		net.Conn
		timeout time.Duration
	}
)

func NewHttpClient(connectTimeout time.Duration, readTimeout time.Duration) *http.Client {
	// Make a copy of the default transport to avoid modifying the global default
	t := http.DefaultTransport.(*http.Transport).Clone()

	// Set the transport's DialContext to use a custom dialer with:
	// 1. the TCP connection timeout
	// 2. a keep-alive period of 30 seconds
	// 3. a custom deadlineConn that applies a read timeout
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{
			Timeout:   connectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &deadlineConn{Conn: conn, timeout: readTimeout}, nil
	}

	// Set the transport's TLSHandshakeTimeout and ResponseHeaderTimeout,
	// so a client will not hang indefinitely if the server does not respond or network goes down.
	t.TLSHandshakeTimeout = connectTimeout
	t.ResponseHeaderTimeout = connectTimeout

	// Set the transport's IdleConnTimeout to close idle connections after 30 seconds,
	// to prevent resource leaks in case of long-lived connections.
	// 30 seconds is a reasonable default since composectl:
	// 1. pulls metadata layers one by one without any significant delay
	// 2. pulls image blobs one by one, without any significant delay
	t.IdleConnTimeout = 30 * time.Second

	// Set the transport's MaxIdleConns and MaxIdleConnsPerHost to limit the number of idle connections.
	// If app blobs are pulled one by one, we don't need to keep many idle connections.
	// Effectively we need just one idle connection per host, so this configuration enables 2 idle connections per host.
	t.MaxIdleConns = 10
	t.MaxIdleConnsPerHost = 2

	// Disable HTTP/2 entirely since it's not needed if usually a single http request is made to pull a blob data and
	// a few sequential requests to authorize a request for a blob or metadata.
	// Also, HTTP/2 require more resources on a client side, so disabling it will reduce memory usage.
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	// Force HTTP/1.1 for TLS ALPN, required for disabling HTTP/2
	t.TLSClientConfig = &tls.Config{
		NextProtos: []string{"http/1.1"},
	}

	return &http.Client{
		Transport: t,
	}
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(b)
}
