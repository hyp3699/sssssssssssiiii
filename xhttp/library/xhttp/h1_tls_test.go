package xhttp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptoRand "crypto/rand"
	gotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

// These tests pin the HTTP/1.1 + TLS path of the client.
//
// decideHTTPVersion picks HTTP/1.1 both for plaintext endpoints (tlsConfig ==
// nil) and for TLS endpoints whose alpn is ["http/1.1"]. The transport must dial
// plaintext for the first case and complete a TLS handshake for the second;
// sending a cleartext GET/POST into a TLS listener makes the download and every
// upload POST fail.

// h1TestTimeout bounds every socket the h1 tests open, so a client that skips
// the handshake fails with a timeout instead of blocking the suite.
const h1TestTimeout = 8 * time.Second

// h1TestDialer dials the loopback network directly, like the client does in
// production through sing's N.Dialer, and puts a deadline on the connection.
type h1TestDialer struct{}

func (h1TestDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, destination.String())
	if err != nil {
		return nil, err
	}
	if err = conn.SetDeadline(time.Now().Add(h1TestTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (h1TestDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return net.ListenPacket("udp", "")
}

var _ N.Dialer = h1TestDialer{}

// h1TestTLSConn adapts *tls.Conn to aTLS.Conn (which also exposes NetConn).
type h1TestTLSConn struct {
	*gotls.Conn
	under net.Conn
}

func (c *h1TestTLSConn) NetConn() net.Conn { return c.under }

func (c *h1TestTLSConn) HandshakeContext(ctx context.Context) error {
	return c.Conn.HandshakeContext(ctx)
}

func (c *h1TestTLSConn) ConnectionState() aTLS.ConnectionState { return c.Conn.ConnectionState() }

// h1TestTLSConfig is a minimal aTLS.Config over a real *tls.Config.
type h1TestTLSConfig struct {
	cfg *gotls.Config
}

func (c *h1TestTLSConfig) ServerName() string              { return c.cfg.ServerName }
func (c *h1TestTLSConfig) SetServerName(serverName string) { c.cfg.ServerName = serverName }
func (c *h1TestTLSConfig) NextProtos() []string            { return c.cfg.NextProtos }
func (c *h1TestTLSConfig) SetNextProtos(nextProto []string) {
	c.cfg.NextProtos = nextProto
}
func (c *h1TestTLSConfig) STDConfig() (*aTLS.STDConfig, error) { return c.cfg.Clone(), nil }

func (c *h1TestTLSConfig) Client(conn net.Conn) (aTLS.Conn, error) {
	return &h1TestTLSConn{Conn: gotls.Client(conn, c.cfg.Clone()), under: conn}, nil
}

func (c *h1TestTLSConfig) Clone() aTLS.Config {
	return &h1TestTLSConfig{cfg: c.cfg.Clone()}
}

var _ aTLS.Config = (*h1TestTLSConfig)(nil)

// h1TestTLSFixture generates a self-signed certificate for 127.0.0.1 and
// returns matched server/client configs, both offering alpn ["http/1.1"].
func h1TestTLSFixture(t *testing.T) (*gotls.Config, *h1TestTLSConfig) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptoRand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(cryptoRand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certificate := gotls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(parsed)
	serverConfig := &gotls.Config{
		Certificates: []gotls.Certificate{certificate},
		NextProtos:   []string{"http/1.1"},
	}
	clientConfig := &gotls.Config{
		RootCAs:    pool,
		ServerName: "localhost",
		NextProtos: []string{"http/1.1"},
	}
	return serverConfig, &h1TestTLSConfig{cfg: clientConfig}
}

// h1TLSServer accepts the first connection, completes the TLS handshake and
// serves one HTTP/1.1 request with 200 OK.
type h1TLSServer struct {
	addr      string
	handshake chan error
	request   chan string
}

func startH1TLSServer(t *testing.T, serverConfig *gotls.Config) *h1TLSServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	server := &h1TLSServer{
		addr:      listener.Addr().String(),
		handshake: make(chan error, 1),
		request:   make(chan string, 1),
	}
	go func() {
		rawConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			server.handshake <- acceptErr
			return
		}
		defer rawConn.Close()
		_ = rawConn.SetDeadline(time.Now().Add(h1TestTimeout))
		tlsConn := gotls.Server(rawConn, serverConfig)
		if handshakeErr := tlsConn.HandshakeContext(context.Background()); handshakeErr != nil {
			server.handshake <- handshakeErr
			return
		}
		server.handshake <- nil
		req, readErr := http.ReadRequest(bufio.NewReader(tlsConn))
		if readErr != nil {
			server.request <- "read error: " + readErr.Error()
			return
		}
		server.request <- req.Method + " " + req.URL.RequestURI()
		io.WriteString(tlsConn, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
	}()
	return server
}

// awaitHandshake reports the server's TLS handshake result. A client that
// skipped the handshake leaves the server blocked reading a ClientHello, so the
// wait is bounded and reported as a failure.
func (s *h1TLSServer) awaitHandshake(t *testing.T) {
	t.Helper()
	select {
	case err := <-s.handshake:
		if err != nil {
			t.Fatalf("server-side TLS handshake failed, client did not speak TLS: %v", err)
		}
	case <-time.After(h1TestTimeout):
		t.Fatal("server-side TLS handshake did not start, client did not speak TLS")
	}
}

func (s *h1TLSServer) awaitRequest(t *testing.T) string {
	t.Helper()
	select {
	case request := <-s.request:
		return request
	case <-time.After(h1TestTimeout):
		t.Fatal("server did not receive an HTTP request")
		return ""
	}
}

// TestHTTP1TransportDialsTLSWithALPN ensures the http.Transport of the
// HTTP/1.1 path performs the TLS handshake on DialTLSContext (used for the
// download GET and for every upload POST).
func TestHTTP1TransportDialsTLSWithALPN(t *testing.T) {
	serverConfig, clientTLS := h1TestTLSFixture(t)
	server := startH1TLSServer(t, serverConfig)

	factory := buildTransportFactory(h1TestDialer{}, clientTLS, 0, "1.1")
	transport, isTransport := factory().(*http.Transport)
	if !isTransport {
		t.Fatalf("HTTP/1.1 factory returned %T, want *http.Transport", factory())
	}
	if transport.DialTLSContext == nil {
		t.Fatal("DialTLSContext is nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), h1TestTimeout)
	defer cancel()

	conn, err := transport.DialTLSContext(ctx, "tcp", server.addr)
	if err != nil {
		t.Fatalf("DialTLSContext: %v", err)
	}
	defer conn.Close()

	// The handshake happens inside DialTLSContext, so the request can be
	// written right away; the server answers only over TLS.
	if _, err = conn.Write([]byte("GET /xhttp?x_padding=1 HTTP/1.1\r\nHost: example.com\r\n\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	server.awaitHandshake(t)
	if got, want := server.awaitRequest(t), "GET /xhttp?x_padding=1"; got != want {
		t.Fatalf("server saw %q, want %q", got, want)
	}
}

// TestHTTP1ConnPoolDialsTLSWithALPN covers the raw-socket upload pool, which
// writes upload POSTs itself instead of going through http.Transport.
func TestHTTP1ConnPoolDialsTLSWithALPN(t *testing.T) {
	serverConfig, clientTLS := h1TestTLSFixture(t)
	server := startH1TLSServer(t, serverConfig)

	serverAddr := M.ParseSocksaddr(server.addr)
	if !serverAddr.IsValid() {
		t.Fatalf("invalid server address %q", server.addr)
	}
	transportSet := newTransportSet(h1TestDialer{}, serverAddr, clientTLS, false, "1.1", 0, nil,
		url.URL{Scheme: "https", Host: server.addr, Path: "/xhttp"}, "example.com")
	if transportSet.h1Pool == nil {
		t.Fatal("HTTP/1.1 transport set has no h1Pool")
	}

	ctx, cancel := context.WithTimeout(context.Background(), h1TestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/xhttp?x_padding=1",
		strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err = transportSet.h1Pool.sendH1Request(ctx, req); err != nil {
		t.Fatalf("sendH1Request: %v", err)
	}
	server.awaitHandshake(t)
	if got, want := server.awaitRequest(t), "POST /xhttp?x_padding=1"; got != want {
		t.Fatalf("server saw %q, want %q", got, want)
	}
}

// TestHTTP1TransportKeepsPlaintextDial pins that an endpoint without TLS still
// dials plaintext: the fix must not start a handshake on a cleartext listener.
func TestHTTP1TransportKeepsPlaintextDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	received := make(chan string, 1)
	go func() {
		rawConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			received <- "accept error: " + acceptErr.Error()
			return
		}
		defer rawConn.Close()
		_ = rawConn.SetDeadline(time.Now().Add(h1TestTimeout))
		line, readErr := bufio.NewReader(rawConn).ReadString('\n')
		if readErr != nil {
			received <- "read error: " + readErr.Error()
			return
		}
		received <- strings.TrimSpace(line)
		io.WriteString(rawConn, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
	}()

	factory := buildTransportFactory(h1TestDialer{}, nil, 0, "1.1")
	transport, isTransport := factory().(*http.Transport)
	if !isTransport {
		t.Fatalf("HTTP/1.1 factory returned %T, want *http.Transport", factory())
	}

	ctx, cancel := context.WithTimeout(context.Background(), h1TestTimeout)
	defer cancel()

	conn, err := transport.DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if _, err = conn.Write([]byte("GET / HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case got := <-received:
		if want := "GET / HTTP/1.1"; got != want {
			t.Fatalf("plaintext server saw %q, want %q", got, want)
		}
	case <-time.After(h1TestTimeout):
		t.Fatal("plaintext server received nothing")
	}
}
