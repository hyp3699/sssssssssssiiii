package v2ray

import (
	"context"

	"github.com/justinwoo280/sing-xhttp/xhttp"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func convertXHTTPOptions(
	options option.V2RayXHTTPOptions,
) (xhttp.Options, error) {
	var result xhttp.Options

	data, err := json.Marshal(options)
	if err != nil {
		return result, err
	}

	if err = json.Unmarshal(data, &result); err != nil {
		return result, err
	}

	return result, nil
}

func NewXHTTPServer(
	ctx context.Context,
	logger logger.ContextLogger,
	options option.V2RayXHTTPOptions,
	tlsConfig tls.ServerConfig,
	handler adapter.V2RayServerTransportHandler,
) (adapter.V2RayServerTransport, error) {
	xhttpOptions, err := convertXHTTPOptions(options)
	if err != nil {
		return nil, err
	}

	return xhttp.NewServer(
		ctx,
		logger,
		xhttpOptions,
		tlsConfig,
		handler,
	)
}

func NewXHTTPClient(
	ctx context.Context,
	dialer N.Dialer,
	serverAddr M.Socksaddr,
	options option.V2RayXHTTPOptions,
	tlsConfig tls.Config,
) (adapter.V2RayClientTransport, error) {
	xhttpOptions, err := convertXHTTPOptions(options)
	if err != nil {
		return nil, err
	}

	return xhttp.NewClient(
		ctx,
		dialer,
		serverAddr,
		xhttpOptions,
		tlsConfig,
	)
}
