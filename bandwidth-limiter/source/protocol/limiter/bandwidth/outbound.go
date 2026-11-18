package bandwidth

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route"

	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

func RegisterOutbound(registry *outbound.Registry) {
	outbound.Register[option.BandwidthLimiterOutboundOptions](registry, C.TypeBandwidthLimiter, NewOutbound)
}

var (
	_ adapter.Lifecycle               = (*Outbound)(nil)
	_ adapter.ConnectionHandler       = (*Outbound)(nil)
	_ adapter.PacketConnectionHandler = (*Outbound)(nil)
)

type Outbound struct {
	outbound.Adapter
	ctx         context.Context
	outbound    adapter.OutboundManager
	connection  adapter.ConnectionManager
	logger      logger.ContextLogger
	strategy    BandwidthStrategy
	outboundTag string
	detour      adapter.Outbound
	// router is the sub-router of this outbound's own `route.rules`. It is nil
	// when the outbound only configures `route.final`: a router without rules
	// would always fall back to the global default outbound, so connections go
	// straight to the detour instead.
	router *route.Router
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.BandwidthLimiterOutboundOptions) (adapter.Outbound, error) {
	if options.Strategy == "" {
		return nil, E.New("missing strategy")
	}
	if options.Route.Final == "" {
		return nil, E.New("missing final outbound")
	}
	if options.Route.Final == tag {
		return nil, E.New("final outbound of outbound/bandwidth-limiter[", tag, "] is itself")
	}
	var strategy BandwidthStrategy
	var err error
	switch options.Strategy {
	case "users":
		usersStrategies := make(map[string]BandwidthStrategy, len(options.Users))
		for _, user := range options.Users {
			if user.Speed == nil {
				return nil, E.New("missing speed for user: ", user.Name)
			}
			userStrategy, err := CreateStrategy(user.Strategy, user.Mode, user.ConnectionType, user.Speed.Value(), options.FlowKeys)
			if err != nil {
				return nil, err
			}
			usersStrategies[user.Name] = userStrategy
		}
		strategy = NewUsersBandwidthStrategy(usersStrategies)
	default:
		if options.Speed == nil {
			return nil, E.New("missing speed")
		}
		strategy, err = CreateStrategy(options.Strategy, options.Mode, options.ConnectionType, options.Speed.Value(), options.FlowKeys)
		if err != nil {
			return nil, err
		}
	}
	outboundManager := service.FromContext[adapter.OutboundManager](ctx)
	var subRouter *route.Router
	if len(options.Route.Rules) > 0 || len(options.Route.RuleSet) > 0 {
		logFactory := service.FromContext[log.Factory](ctx)
		subRouter = route.NewRouter(routerContext(ctx, options.Route.Final), logFactory, options.Route, option.DNSOptions{})
		err = subRouter.Initialize(options.Route.Rules, options.Route.RuleSet)
		if err != nil {
			return nil, err
		}
	}
	outbound := &Outbound{
		Adapter:     outbound.NewAdapter(C.TypeBandwidthLimiter, tag, nil, outboundDependencies(tag, options)),
		ctx:         ctx,
		outbound:    outboundManager,
		connection:  service.FromContext[adapter.ConnectionManager](ctx),
		logger:      logger,
		strategy:    strategy,
		outboundTag: options.Route.Final,
		router:      subRouter,
	}
	return outbound, nil
}

// routerContext returns a child context whose OutboundManager reports this
// outbound's `route.final` as Default(). route.Router resolves the outbound of
// every matched rule with Outbound(tag) — unchanged — but uses Default() when no
// rule matches, and that fallback must be this outbound's final outbound, not
// the global one. The global default can even be this limiter itself, which
// would route the connection back into it forever.
//
// The registry is cloned so the substitution stays local to this sub-router.
func routerContext(ctx context.Context, finalTag string) context.Context {
	registry := service.RegistryFromContext(ctx)
	if registry == nil {
		return ctx
	}
	outboundManager := service.FromContext[adapter.OutboundManager](ctx)
	if outboundManager == nil {
		return ctx
	}
	return service.ContextWith[adapter.OutboundManager](
		service.ContextWithRegistry(ctx, registry.Clone()),
		adapter.OutboundManager(&finalOutboundManager{OutboundManager: outboundManager, finalTag: finalTag}),
	)
}

type finalOutboundManager struct {
	adapter.OutboundManager
	finalTag string
}

func (m *finalOutboundManager) Default() adapter.Outbound {
	if finalOutbound, loaded := m.OutboundManager.Outbound(m.finalTag); loaded {
		return finalOutbound
	}
	return m.OutboundManager.Default()
}

// outboundDependencies lists the outbound tags this outbound refers to, so
// OutboundManager starts them first: the final outbound itself plus every
// outbound named by a route rule, descending into logical rules.
//
// The tag of the limiter itself is skipped: a rule may route back into this
// limiter (a deliberately chained limiter) but a self-dependency would only be
// reported as a circular dependency.
func outboundDependencies(tag string, options option.BandwidthLimiterOutboundOptions) []string {
	var dependencies []string
	seen := make(map[string]bool)
	addDependency := func(dependencyTag string) {
		if dependencyTag == "" || dependencyTag == tag || seen[dependencyTag] {
			return
		}
		seen[dependencyTag] = true
		dependencies = append(dependencies, dependencyTag)
	}
	addDependency(options.Route.Final)
	var addRules func(rules []option.Rule)
	addRules = func(rules []option.Rule) {
		for _, rule := range rules {
			switch rule.Type {
			case C.RuleTypeDefault:
				addDependency(rule.DefaultOptions.RouteOptions.Outbound)
				addDependency(rule.DefaultOptions.BypassOptions.Outbound)
			case C.RuleTypeLogical:
				addDependency(rule.LogicalOptions.RouteOptions.Outbound)
				addDependency(rule.LogicalOptions.BypassOptions.Outbound)
				addRules(rule.LogicalOptions.Rules)
			}
		}
	}
	addRules(options.Route.Rules)
	return dependencies
}

func (h *Outbound) Network() []string {
	return []string{N.NetworkTCP, N.NetworkUDP}
}

func (h *Outbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage == adapter.StartStateStart {
		detour, loaded := h.outbound.Outbound(h.outboundTag)
		if !loaded {
			return E.New("outbound not found: ", h.outboundTag)
		}
		h.detour = detour
	}
	if h.router != nil {
		return h.router.Start(stage, scope)
	}
	return nil
}

func (h *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := h.detour.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	wrappedConn, err := h.strategy.wrapConn(ctx, conn, adapter.ContextFrom(ctx), true)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return wrappedConn, nil
}

func (h *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	conn, err := h.detour.ListenPacket(ctx, destination)
	if err != nil {
		return nil, err
	}
	wrappedConn, err := h.strategy.wrapPacketConn(ctx, conn, adapter.ContextFrom(ctx), true)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return wrappedConn, nil
}

func (h *Outbound) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = adapter.WithContext(ctx, &metadata)
	wrappedConn, err := h.strategy.wrapConn(ctx, conn, &metadata, false)
	if err != nil {
		h.logger.ErrorContext(ctx, err)
		N.CloseOnHandshakeFailure(conn, onClose, err)
		return
	}
	if h.router == nil {
		h.forwardConnection(ctx, wrappedConn, metadata, onClose)
		return
	}
	h.router.RouteConnectionEx(ctx, wrappedConn, metadata, onClose)
}

func (h *Outbound) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = adapter.WithContext(ctx, &metadata)
	packetConn, err := h.strategy.wrapPacketConn(ctx, bufio.NewNetPacketConn(conn), &metadata, false)
	if err != nil {
		h.logger.ErrorContext(ctx, err)
		N.CloseOnHandshakeFailure(conn, onClose, err)
		return
	}
	if h.router == nil {
		h.forwardPacketConnection(ctx, bufio.NewPacketConn(packetConn), metadata, onClose)
		return
	}
	h.router.RoutePacketConnectionEx(ctx, bufio.NewPacketConn(packetConn), metadata, onClose)
}

// forwardConnection delivers an already-wrapped connection to `route.final`.
//
// The caller's router has already applied its connection trackers, FakeIP NAT
// and interrupt registration to this connection before handing it here, so this
// path deliberately does none of that again: it only records the target
// outbound in the metadata and hands the connection to that outbound, in the
// same two ways route.Router would (handler outbounds get the connection
// directly, plain dialers get it through the connection manager).
func (h *Outbound) forwardConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Network = N.NetworkTCP
	metadata.RouteOutbound = h.outboundTag
	metadata.OutboundChain = []adapter.Outbound{h.detour}
	if handler, isHandler := h.detour.(adapter.ConnectionHandler); isHandler {
		handler.NewConnection(ctx, conn, metadata, onClose)
		return
	}
	h.connection.NewConnection(ctx, h.detour, conn, metadata, onClose)
}

func (h *Outbound) forwardPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	metadata.Network = N.NetworkUDP
	metadata.RouteOutbound = h.outboundTag
	metadata.OutboundChain = []adapter.Outbound{h.detour}
	if handler, isHandler := h.detour.(adapter.PacketConnectionHandler); isHandler {
		handler.NewPacketConnection(ctx, conn, metadata, onClose)
		return
	}
	h.connection.NewPacketConnection(ctx, h.detour, conn, metadata, onClose)
}

func (h *Outbound) GetStrategy() BandwidthStrategy {
	return h.strategy
}
