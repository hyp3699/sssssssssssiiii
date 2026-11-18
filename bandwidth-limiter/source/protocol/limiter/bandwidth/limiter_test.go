package bandwidth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	"github.com/sagernet/sing/common/byteformats"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"golang.org/x/time/rate"
)

func mustSpeed(t *testing.T, value string) *byteformats.NetworkBytesCompat {
	t.Helper()
	var speed byteformats.NetworkBytesCompat
	if err := json.Unmarshal([]byte(`"`+value+`"`), &speed); err != nil {
		t.Fatal(err)
	}
	return &speed
}

func fixedIDGetter(id string) ConnIDGetter {
	return func(context.Context, *adapter.InboundContext) (string, bool) {
		return id, true
	}
}

// stubLimiter reports the configured error after the configured delay.
type stubLimiter struct {
	err   error
	delay time.Duration
	calls int
}

func (l *stubLimiter) WaitN(ctx context.Context, n int) error {
	l.calls++
	if l.delay > 0 {
		select {
		case <-time.After(l.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return l.err
}

func (l *stubLimiter) SetSpeed(uint64) {}

// TestSpeedLimiterChunksAboveBurst pins that a request larger than the limiter's
// burst is consumed in pieces instead of failing: rate.Limiter.WaitN rejects
// such a request with "exceeds limiter's burst", which used to drop the
// connection whenever a single Read/Write carried more than 64 KiB.
func TestSpeedLimiterChunksAboveBurst(t *testing.T) {
	limiter := &speedLimiter{limiter: rate.NewLimiter(rate.Inf, 65536)}
	ctx := context.Background()

	for _, size := range []int{0, -1, 1, 65536, 65537, 128 * 1024, 1 << 20} {
		if err := limiter.WaitN(ctx, size); err != nil {
			t.Fatalf("WaitN(%d) = %v, want nil", size, err)
		}
	}

	// A finite but high rate, so the wait is a real reservation rather than
	// the infinite-rate fast path.
	finite := &speedLimiter{limiter: rate.NewLimiter(rate.Limit(1<<30), 65536)}
	deadlineCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := finite.WaitN(deadlineCtx, 1<<20); err != nil {
		t.Fatalf("WaitN(1 MiB) = %v, want nil", err)
	}
}

// TestSpeedLimiterZeroBurstFallsBack guards the burst<=0 fallback: without it the
// chunk loop would never advance and spin forever.
func TestSpeedLimiterZeroBurstFallsBack(t *testing.T) {
	limiter := &speedLimiter{limiter: rate.NewLimiter(rate.Inf, 0)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := limiter.WaitN(ctx, 16); err != nil {
		t.Fatalf("WaitN with zero burst = %v, want nil", err)
	}
}

// TestConnLimiterLargeWrite checks the same fix through the conn wrapper used for
// real traffic: a single Write of 128 KiB must be throttled, not rejected.
func TestConnLimiterLargeWrite(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	written := make(chan error, 1)
	go func() {
		limiter := &speedLimiter{limiter: rate.NewLimiter(rate.Limit(1<<30), 65536)}
		wrapped := NewConnWithUploadBandwidthLimiter(context.Background(), clientConn, limiter)
		payload := make([]byte, 128*1024)
		_, err := wrapped.Write(payload)
		written <- err
	}()

	go func() {
		_, _ = io.Copy(io.Discard, serverConn)
	}()

	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("Write = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Write did not finish")
	}
}

// TestFairQueueLimiterPropagatesLimiterError pins that an error from the
// underlying limiter reaches the caller instead of being reported as success.
func TestFairQueueLimiterPropagatesLimiterError(t *testing.T) {
	expected := errors.New("limiter rejected the request")
	underlying := &stubLimiter{err: expected}
	limiter := NewFairQueueLimiter(fixedIDGetter("user-1"), underlying)

	err := limiter.WaitN(context.Background(), 1024)
	if !errors.Is(err, expected) {
		t.Fatalf("WaitN = %v, want %v", err, expected)
	}
	if underlying.calls != 1 {
		t.Fatalf("underlying calls = %d, want 1", underlying.calls)
	}
}

// TestFairQueueLimiterReportsCanceledWait pins that a wait canceled inside the
// underlying limiter is reported to the caller as canceled.
func TestFairQueueLimiterReportsCanceledWait(t *testing.T) {
	underlying := &stubLimiter{err: context.Canceled}
	limiter := NewFairQueueLimiter(fixedIDGetter("user-1"), underlying)

	if err := limiter.WaitN(context.Background(), 512); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitN = %v, want %v", err, context.Canceled)
	}
}

// TestFairQueueLimiterQueuedRequestsShareResult checks the queued path: a request
// served by another waiter's turn must still see its own limiter result.
func TestFairQueueLimiterQueuedRequestsShareResult(t *testing.T) {
	expected := errors.New("queue drained with error")
	underlying := &stubLimiter{err: expected, delay: 10 * time.Millisecond}
	limiter := NewFairQueueLimiter(fixedIDGetter("shared"), underlying)

	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			results <- limiter.WaitN(context.Background(), 4096)
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, expected) {
				t.Fatalf("WaitN = %v, want %v", err, expected)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("WaitN did not return")
		}
	}
}

// TestOutboundDependencies covers the dependency list the outbound manager needs
// to start this outbound's targets first.
func TestOutboundDependencies(t *testing.T) {
	logicalRule := option.Rule{
		Type: C.RuleTypeLogical,
		LogicalOptions: option.LogicalRule{
			RawLogicalRule: option.RawLogicalRule{
				Mode: C.LogicalTypeOr,
				Rules: []option.Rule{
					{
						Type: C.RuleTypeDefault,
						DefaultOptions: option.DefaultRule{
							RuleAction: option.RuleAction{
								Action:       C.RuleActionTypeRoute,
								RouteOptions: option.RouteActionOptions{Outbound: "nested"},
							},
						},
					},
					{
						Type: C.RuleTypeDefault,
						DefaultOptions: option.DefaultRule{
							RuleAction: option.RuleAction{
								Action:        C.RuleActionTypeBypass,
								BypassOptions: option.RouteActionOptions{Outbound: "bypassed"},
							},
						},
					},
				},
			},
			RuleAction: option.RuleAction{
				Action:       C.RuleActionTypeRoute,
				RouteOptions: option.RouteActionOptions{Outbound: "logical"},
			},
		},
	}
	options := option.BandwidthLimiterOutboundOptions{
		Route: option.RouteOptions{
			Final: "final",
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RuleAction: option.RuleAction{
							Action:       C.RuleActionTypeRoute,
							RouteOptions: option.RouteActionOptions{Outbound: "first"},
						},
					},
				},
				logicalRule,
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RuleAction: option.RuleAction{
							Action:       C.RuleActionTypeRoute,
							RouteOptions: option.RouteActionOptions{Outbound: "first"},
						},
					},
				},
			},
		},
	}
	dependencies := outboundDependencies("limiter", options)
	expected := []string{"final", "first", "logical", "nested", "bypassed"}
	if len(dependencies) != len(expected) {
		t.Fatalf("dependencies = %v, want %v", dependencies, expected)
	}
	for i := range expected {
		if dependencies[i] != expected[i] {
			t.Fatalf("dependencies = %v, want %v", dependencies, expected)
		}
	}

	// The limiter's own tag is never a dependency: a rule may chain back into
	// this limiter, but the final outbound must not be itself.
	selfReferencing := option.BandwidthLimiterOutboundOptions{Route: option.RouteOptions{Final: "limiter"}}
	if dependencies := outboundDependencies("limiter", selfReferencing); len(dependencies) != 0 {
		t.Fatalf("self dependencies = %v, want none", dependencies)
	}
}

// TestFinalOutboundManagerDefault pins the fallback the sub-router uses when no
// rule matches: route.final, not the global default.
func TestFinalOutboundManagerDefault(t *testing.T) {
	finalOutbound := newFakeOutbound("final")
	globalDefault := newFakeOutbound("global")
	manager := &fakeOutboundManager{
		byTag:           map[string]adapter.Outbound{"final": finalOutbound},
		defaultOutbound: globalDefault,
	}

	wrapped := &finalOutboundManager{OutboundManager: manager, finalTag: "final"}
	if got := wrapped.Default(); got != finalOutbound {
		t.Fatalf("Default() = %v, want the final outbound", got)
	}
	// A missing tag keeps the previous behaviour instead of returning nil.
	missing := &finalOutboundManager{OutboundManager: manager, finalTag: "missing"}
	if got := missing.Default(); got != globalDefault {
		t.Fatalf("Default() with an unknown tag = %v, want the global default", got)
	}
}

// TestOutboundWithoutRulesRoutesToFinal covers the common configuration:
// `"route": {"final": "direct"}` only. The connection must reach that outbound
// rather than the global default, and no sub-router may be created for it.
func TestOutboundWithoutRulesRoutesToFinal(t *testing.T) {
	finalOutbound := newFakeOutbound("direct")
	outboundManager := &fakeOutboundManager{
		byTag:           map[string]adapter.Outbound{"direct": finalOutbound},
		defaultOutbound: newFakeOutbound("global"),
	}
	connectionManager := &recordingConnectionManager{called: make(chan struct{}, 1)}
	ctx := service.ContextWithRegistry(context.Background(), service.NewRegistry())
	ctx = service.ContextWith[adapter.OutboundManager](ctx, outboundManager)
	ctx = service.ContextWith[adapter.ConnectionManager](ctx, connectionManager)
	ctx = service.ContextWith[log.Factory](ctx, log.NewNOPFactory())

	instance, err := NewOutbound(ctx, nil, logger.NOP(), "limit-users", option.BandwidthLimiterOutboundOptions{
		Strategy: "global",
		Mode:     "bidirectional",
		Speed:    mustSpeed(t, "10MB"),
		Route:    option.RouteOptions{Final: "direct"},
	})
	if err != nil {
		t.Fatal(err)
	}
	limiter, isLimiter := instance.(*Outbound)
	if !isLimiter {
		t.Fatalf("NewOutbound returned %T", instance)
	}
	if limiter.router != nil {
		t.Fatal("a router was created for an outbound without rules")
	}
	if dependencies := limiter.Dependencies(); len(dependencies) != 1 || dependencies[0] != "direct" {
		t.Fatalf("dependencies = %v, want [direct]", dependencies)
	}

	scope := adapter.NewScope(ctx, logger.NOP())
	defer scope.Close()
	if err = limiter.Start(adapter.StartStateStart, scope); err != nil {
		t.Fatal(err)
	}
	if limiter.detour != finalOutbound {
		t.Fatalf("detour = %v, want the final outbound", limiter.detour)
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	metadata := adapter.InboundContext{
		User:        "vless-xhttp-user1",
		Destination: M.ParseSocksaddrHostPort("example.com", 443),
	}
	limiter.NewConnection(ctx, clientConn, metadata, nil)

	select {
	case <-connectionManager.called:
	case <-time.After(10 * time.Second):
		t.Fatal("the connection was never forwarded")
	}
	if connectionManager.this != finalOutbound {
		t.Fatalf("forwarded to %v, want the final outbound", connectionManager.this)
	}
	if connectionManager.metadata.RouteOutbound != "direct" {
		t.Fatalf("metadata.RouteOutbound = %q, want %q", connectionManager.metadata.RouteOutbound, "direct")
	}
	if len(connectionManager.metadata.OutboundChain) != 1 || connectionManager.metadata.OutboundChain[0] != finalOutbound {
		t.Fatalf("metadata.OutboundChain = %v, want the final outbound", connectionManager.metadata.OutboundChain)
	}
}

// TestOutboundRejectsSelfFinal guards against a configuration that would route
// the limiter back into itself.
func TestOutboundRejectsSelfFinal(t *testing.T) {
	options := option.BandwidthLimiterOutboundOptions{
		Strategy: "global",
		Mode:     "bidirectional",
		Speed:    mustSpeed(t, "1MB"),
		Route:    option.RouteOptions{Final: "limit-users"},
	}
	if _, err := NewOutbound(context.Background(), nil, logger.NOP(), "limit-users", options); err == nil {
		t.Fatal("NewOutbound accepted route.final pointing at the limiter itself")
	}
}

// --- fakes -------------------------------------------------------------

type fakeOutbound struct {
	tag string
}

var _ adapter.Outbound = (*fakeOutbound)(nil)

func newFakeOutbound(tag string) *fakeOutbound {
	return &fakeOutbound{tag: tag}
}

func (o *fakeOutbound) Type() string           { return C.TypeDirect }
func (o *fakeOutbound) Tag() string            { return o.tag }
func (o *fakeOutbound) Network() []string      { return []string{N.NetworkTCP, N.NetworkUDP} }
func (o *fakeOutbound) Dependencies() []string { return nil }

func (o *fakeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("fake outbound cannot dial")
}

func (o *fakeOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("fake outbound cannot listen")
}

type fakeOutboundManager struct {
	byTag           map[string]adapter.Outbound
	defaultOutbound adapter.Outbound
}

func (m *fakeOutboundManager) Start(adapter.StartStage, *adapter.Scope) error { return nil }

func (m *fakeOutboundManager) Outbounds() []adapter.Outbound {
	outbounds := make([]adapter.Outbound, 0, len(m.byTag))
	for _, outbound := range m.byTag {
		outbounds = append(outbounds, outbound)
	}
	return outbounds
}

func (m *fakeOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	outbound, loaded := m.byTag[tag]
	return outbound, loaded
}

func (m *fakeOutboundManager) Default() adapter.Outbound { return m.defaultOutbound }

func (m *fakeOutboundManager) Create(context.Context, adapter.Router, log.ContextLogger, string, string, any) error {
	return errors.New("fake outbound manager cannot create outbounds")
}

type recordingConnectionManager struct {
	called   chan struct{}
	this     N.Dialer
	metadata adapter.InboundContext
}

func (m *recordingConnectionManager) Start(adapter.StartStage, *adapter.Scope) error { return nil }
func (m *recordingConnectionManager) Count() int                                     { return 0 }
func (m *recordingConnectionManager) CloseAll()                                      {}
func (m *recordingConnectionManager) TrackConn(conn net.Conn) net.Conn               { return conn }
func (m *recordingConnectionManager) TrackPacketConn(conn net.PacketConn) net.PacketConn {
	return conn
}

func (m *recordingConnectionManager) NewConnection(ctx context.Context, this N.Dialer, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	m.this = this
	m.metadata = metadata
	m.called <- struct{}{}
}

func (m *recordingConnectionManager) NewPacketConnection(ctx context.Context, this N.Dialer, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	m.this = this
	m.metadata = metadata
	m.called <- struct{}{}
}
