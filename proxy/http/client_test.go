package http

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport/internet/stat"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
)

type httpTestDialer struct {
	conn  net.Conn
	dials int
}

func (d *httpTestDialer) Dial(context.Context, net.Destination) (stat.Connection, error) {
	d.dials++
	return d.conn, nil
}

func (*httpTestDialer) DestIpAddress() net.IP { return nil }

func (*httpTestDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestHTTP1ConnectHeaders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []*Header
		account *Account
		want    []string
	}{
		{
			name: "defaults",
			want: []string{
				"Host: target.example:443",
				"User-Agent: Go-http-client/1.1",
				"Proxy-Connection: Keep-Alive",
			},
		},
		{
			name: "literal overrides and spelling",
			headers: []*Header{
				{Key: "Host", Value: "front.example"},
				{Key: "User-Agent", Value: "firefox"},
				{Key: "Proxy-Connection", Value: "close"},
				{Key: "x-MiXeD", Value: "  padded\t "},
				{Key: "X-Empty", Value: ""},
				{Key: "X-Template", Value: "{{.Target}} {{unclosed"},
			},
			want: []string{
				"Host: front.example",
				"User-Agent: firefox",
				"Proxy-Connection: close",
				"x-MiXeD:   padded\t ",
				"X-Empty: ",
				"X-Template: {{.Target}} {{unclosed",
			},
		},
		{
			name: "case sensitive defaults",
			headers: []*Header{
				{Key: "host", Value: "front.example"},
				{Key: "user-agent", Value: "custom"},
				{Key: "proxy-connection", Value: "close"},
			},
			want: []string{
				"Host: target.example:443",
				"User-Agent: Go-http-client/1.1",
				"Proxy-Connection: Keep-Alive",
				"host: front.example",
				"user-agent: custom",
				"proxy-connection: close",
			},
		},
		{
			name:    "empty user agent is sent",
			headers: []*Header{{Key: "User-Agent", Value: ""}},
			want: []string{
				"Host: target.example:443",
				"User-Agent: ",
				"Proxy-Connection: Keep-Alive",
			},
		},
		{
			name:    "custom authentication",
			headers: []*Header{{Key: "Proxy-Authorization", Value: "Bearer custom"}},
			want: []string{
				"Host: target.example:443",
				"User-Agent: Go-http-client/1.1",
				"Proxy-Connection: Keep-Alive",
				"Proxy-Authorization: Bearer custom",
			},
		},
		{
			name:    "credentials override custom authentication",
			headers: []*Header{{Key: "Proxy-Authorization", Value: "Bearer custom"}},
			account: &Account{Username: "user", Password: "pass"},
			want: []string{
				"Host: target.example:443",
				"User-Agent: Go-http-client/1.1",
				"Proxy-Connection: Keep-Alive",
				"Proxy-Authorization: Basic dXNlcjpwYXNz",
			},
		},
		{
			name:    "empty password preserves custom authentication",
			headers: []*Header{{Key: "Proxy-Authorization", Value: "Bearer custom"}},
			account: &Account{Username: "user"},
			want: []string{
				"Host: target.example:443",
				"User-Agent: Go-http-client/1.1",
				"Proxy-Connection: Keep-Alive",
				"Proxy-Authorization: Bearer custom",
			},
		},
		{
			name:    "empty username does not generate authentication",
			account: &Account{Password: "pass"},
			want: []string{
				"Host: target.example:443",
				"User-Agent: Go-http-client/1.1",
				"Proxy-Connection: Keep-Alive",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := stdnet.Pipe()
			t.Cleanup(func() { client.Close() })
			t.Cleanup(func() { server.Close() })
			deadline := time.Now().Add(5 * time.Second)
			client.SetDeadline(deadline)
			server.SetDeadline(deadline)

			type result struct {
				lines []string
				err   error
			}
			done := make(chan result, 1)
			go func() {
				reader := bufio.NewReader(server)
				var lines []string
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						done <- result{err: err}
						return
					}
					if line == "\r\n" {
						break
					}
					lines = append(lines, strings.TrimSuffix(line, "\r\n"))
				}
				_, err := io.WriteString(server, "HTTP/1.1 200 Connection established\r\nContent-Length: 0\r\n\r\ngreeting")
				done <- result{lines: lines, err: err}
			}()

			var user *protocol.MemoryUser
			if tc.account != nil {
				user = &protocol.MemoryUser{Account: tc.account}
			}
			conn, err := setUpHTTPTunnel(context.Background(), net.TCPDestination(net.LocalHostIP, 8080), "target.example:443", user, &httpTestDialer{conn: client}, tc.headers, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			greeting := make([]byte, len("greeting"))
			if _, err := io.ReadFull(conn, greeting); err != nil || string(greeting) != "greeting" {
				t.Fatalf("tunnel greeting = %q, err = %v", greeting, err)
			}
			got := <-done
			if got.err != nil {
				t.Fatal(got.err)
			}
			if len(got.lines) == 0 || got.lines[0] != "CONNECT target.example:443 HTTP/1.1" {
				t.Fatalf("unexpected request: %q", got.lines)
			}
			slices.Sort(got.lines[1:])
			slices.Sort(tc.want)
			if !slices.Equal(got.lines[1:], tc.want) {
				t.Errorf("headers = %q, want %q", got.lines[1:], tc.want)
			}
		})
	}
}

func TestHTTP2ConnectHeadersAndReuse(t *testing.T) {
	type request struct {
		method string
		host   string
		header http.Header
		err    error
	}
	requests := make(chan request, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		payload := make([]byte, len("first payload"))
		_, err := io.ReadFull(r.Body, payload)
		requests <- request{method: r.Method, host: r.Host, header: r.Header.Clone(), err: err}
		if err == nil {
			w.Write(payload)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	rawConn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	rawConn.SetDeadline(time.Now().Add(5 * time.Second))
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dialer := &httpTestDialer{conn: xtls.Client(rawConn, &tls.Config{
		RootCAs:    roots,
		ServerName: "example.com",
		NextProtos: []string{"h2"},
	})}
	dest, err := net.ParseDestination("tcp:" + server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rawConn.Close()
		cachedH2Mutex.Lock()
		delete(cachedH2Conns, dest)
		cachedH2Mutex.Unlock()
	})
	user := &protocol.MemoryUser{Account: &Account{Username: "user", Password: "pass"}}
	for _, tc := range []struct {
		headers   []*Header
		userAgent string
	}{
		{
			headers: []*Header{
				{Key: "Host", Value: "front.example"},
				{Key: "User-Agent", Value: "firefox"},
				{Key: "Proxy-Connection", Value: "close"},
				{Key: "Proxy-Authorization", Value: "Bearer custom"},
				{Key: "x-MiXeD", Value: "{{.Target}}"},
			},
			userAgent: "firefox",
		},
		{userAgent: "Go-http-client/1.1"},
	} {
		conn, err := setUpHTTPTunnel(context.Background(), dest, "target.example:443", user, dialer, tc.headers, []byte("first payload"))
		if err != nil {
			t.Fatal(err)
		}
		payload := make([]byte, len("first payload"))
		_, err = io.ReadFull(conn, payload)
		conn.Close()
		if err != nil || string(payload) != "first payload" {
			t.Fatalf("tunnel payload = %q, err = %v", payload, err)
		}
		got := <-requests
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.method != http.MethodConnect || got.host != "target.example:443" {
			t.Errorf("request = %s %s", got.method, got.host)
		}
		if got.header.Get("Proxy-Connection") != "" {
			t.Errorf("HTTP/2 included Proxy-Connection: %q", got.header)
		}
		if got.header.Get("User-Agent") != tc.userAgent || got.header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			t.Errorf("unexpected headers: %q", got.header)
		}
		if tc.headers != nil && got.header.Get("X-Mixed") != "{{.Target}}" {
			t.Errorf("custom literal header was lost: %q", got.header)
		}
	}
	if dialer.dials != 1 {
		t.Errorf("dialed %d connections, want one reused HTTP/2 connection", dialer.dials)
	}
}
