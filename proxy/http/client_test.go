package http

import (
	"bufio"
	"context"
	"io"
	"net"
	stdhttp "net/http"
	"testing"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type pipeDialer struct {
	conn net.Conn
}

func (d pipeDialer) Dial(context.Context, xnet.Destination) (stat.Connection, error) {
	return d.conn, nil
}

func (pipeDialer) DestIpAddress() xnet.IP {
	return nil
}

func (pipeDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

func TestSetUpHTTPTunnelUsesConfiguredHostHeader(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	requestCh := make(chan *stdhttp.Request, 1)
	serverErrCh := make(chan error, 1)
	go func() {
		request, err := stdhttp.ReadRequest(bufio.NewReader(serverConn))
		if err != nil {
			serverErrCh <- err
			return
		}
		requestCh <- request
		_, err = io.WriteString(serverConn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		serverErrCh <- err
	}()

	proxyConn, err := setUpHTTPTunnel(
		context.Background(),
		xnet.TCPDestination(xnet.DomainAddress("proxy.example"), 8080),
		"target.example:443",
		nil,
		pipeDialer{conn: clientConn},
		[]*Header{
			{Key: "hOsT", Value: "front.example"},
			{Key: "User-Agent", Value: "custom-agent"},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer proxyConn.Close()

	request := <-requestCh
	if request.Host != "front.example" {
		t.Fatalf("unexpected CONNECT Host: got %q, want %q", request.Host, "front.example")
	}
	if userAgent := request.Header.Get("User-Agent"); userAgent != "custom-agent" {
		t.Fatalf("unexpected User-Agent: got %q, want %q", userAgent, "custom-agent")
	}
	if err := <-serverErrCh; err != nil {
		t.Fatal(err)
	}
}
