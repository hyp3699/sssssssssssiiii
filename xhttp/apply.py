#!/usr/bin/env python3
"""Apply the XHTTP transport integration to an official SagerNet/sing-box
source tree.

The XHTTP library itself is vendored in this repository under
``xhttp/library/`` (upstream: github.com/justinwoo280/sing-xhttp, see
``xhttp/VERSION`` / ``xhttp/UPSTREAM.md``). The workflows point sing-box's
go.mod at that local copy with a ``replace`` directive; this script only
applies the sing-box-side glue that used to live inline in the build
workflow YAML:

1. copy ``source/transport/v2ray/xhttp.go`` (the bridge to the library)
   into the sing-box tree,
2. add the ``xhttp`` transport type constant,
3. add the ``V2RayXHTTPOptions`` option structures and their
   marshal/unmarshal/schema registration (mirrors ``xhttp.Options`` in
   ``library/xhttp/options.go`` — keep the two in sync),
4. dispatch ``xhttp`` in ``transport/v2ray/transport.go``.

Usage (run inside the sing-box source root):
    python3 <this-dir>/apply.py
"""
import shutil
import sys
from pathlib import Path
from textwrap import dedent

HERE = Path(__file__).resolve().parent
SRC = HERE / "source"
ROOT = Path.cwd()


def die(msg):
    raise SystemExit(f"ERROR: {msg}")


def replace_exact(path, old, new, expected=1):
    p = ROOT / path
    source = p.read_text()
    count = source.count(old)
    if count != expected:
        die(f"{path}: expected {expected} occurrence(s), found {count}")
    p.write_text(source.replace(old, new, expected))
    print(f"PATCHED: {path}")


def copy_exact(rel):
    src = SRC / rel
    dst = ROOT / rel
    if dst.exists():
        die(f"{rel} already exists in sing-box tree")
    dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(src, dst)
    print(f"CREATED: {rel}")


# ----------------------------------------------------------------------------
# 1. bridge file: transport/v2ray/xhttp.go
# ----------------------------------------------------------------------------

copy_exact("transport/v2ray/xhttp.go")

# ----------------------------------------------------------------------------
# 2. constant/v2ray.go
# ----------------------------------------------------------------------------

replace_exact(
    "constant/v2ray.go",
    'V2RayTransportTypeHTTPUpgrade = "httpupgrade"\n',
    'V2RayTransportTypeHTTPUpgrade = "httpupgrade"\n'
    'V2RayTransportTypeXHTTP        = "xhttp"\n',
)

# ----------------------------------------------------------------------------
# 3. option/v2ray_transport.go
# ----------------------------------------------------------------------------

replace_exact(
    "option/v2ray_transport.go",
    '\tType               string                  `json:"type" enum:"http,ws,quic,grpc,httpupgrade"`\n'
    '\tHTTPOptions        V2RayHTTPOptions        `json:"-"`\n'
    '\tWebsocketOptions   V2RayWebsocketOptions   `json:"-"`\n'
    '\tQUICOptions        V2RayQUICOptions        `json:"-"`\n'
    '\tGRPCOptions        V2RayGRPCOptions        `json:"-"`\n'
    '\tHTTPUpgradeOptions V2RayHTTPUpgradeOptions `json:"-"`\n',
    '\tType               string                  `json:"type" enum:"http,ws,quic,grpc,httpupgrade,xhttp"`\n'
    '\tHTTPOptions        V2RayHTTPOptions        `json:"-"`\n'
    '\tWebsocketOptions   V2RayWebsocketOptions   `json:"-"`\n'
    '\tQUICOptions        V2RayQUICOptions        `json:"-"`\n'
    '\tGRPCOptions        V2RayGRPCOptions        `json:"-"`\n'
    '\tHTTPUpgradeOptions V2RayHTTPUpgradeOptions `json:"-"`\n'
    '\tXHTTPOptions       V2RayXHTTPOptions       `json:"-"`\n',
)

# MarshalJSON
replace_exact(
    "option/v2ray_transport.go",
    '\tcase C.V2RayTransportTypeHTTPUpgrade:\n'
    '\t\tv = o.HTTPUpgradeOptions\n'
    '\tcase "":\n',
    '\tcase C.V2RayTransportTypeHTTPUpgrade:\n'
    '\t\tv = o.HTTPUpgradeOptions\n'
    '\tcase C.V2RayTransportTypeXHTTP:\n'
    '\t\tv = o.XHTTPOptions\n'
    '\tcase "":\n',
)

# UnmarshalJSON
replace_exact(
    "option/v2ray_transport.go",
    '\tcase C.V2RayTransportTypeHTTPUpgrade:\n'
    '\t\tv = &o.HTTPUpgradeOptions\n'
    '\tdefault:\n',
    '\tcase C.V2RayTransportTypeHTTPUpgrade:\n'
    '\t\tv = &o.HTTPUpgradeOptions\n'
    '\tcase C.V2RayTransportTypeXHTTP:\n'
    '\t\tv = &o.XHTTPOptions\n'
    '\tdefault:\n',
)

# Schema
replace_exact(
    "option/v2ray_transport.go",
    '\t\t\t{Value: C.V2RayTransportTypeHTTPUpgrade, StructType: reflect.TypeFor[V2RayHTTPUpgradeOptions]()},\n'
    '\t\t}, nil)\n',
    '\t\t\t{Value: C.V2RayTransportTypeHTTPUpgrade, StructType: reflect.TypeFor[V2RayHTTPUpgradeOptions]()},\n'
    '\t\t\t{Value: C.V2RayTransportTypeXHTTP, StructType: reflect.TypeFor[V2RayXHTTPOptions]()},\n'
    '\t\t}, nil)\n',
)

# XHTTP option structures. Mirrors sing-xhttp v0.1.6 (library/xhttp/options.go).
marker = "type V2RayHTTPUpgradeOptions struct {\n"

mirror = dedent(
    '''
    type V2RayXHTTPRange struct {
        From int32 `json:"from,omitempty"`
        To   int32 `json:"to,omitempty"`
    }

    type V2RayXHTTPDownloadConfig struct {
        Host string `json:"host,omitempty"`
        Path string `json:"path,omitempty"`
    }

    type V2RayXHTTPXmuxConfig struct {
        MaxConcurrency   *V2RayXHTTPRange `json:"max_concurrency,omitempty"`
        MaxConnections   *V2RayXHTTPRange `json:"max_connections,omitempty"`
        CMaxReuseTimes   *V2RayXHTTPRange `json:"c_max_reuse_times,omitempty"`
        HMaxRequestTimes *V2RayXHTTPRange `json:"h_max_request_times,omitempty"`
        HMaxReusableSecs *V2RayXHTTPRange `json:"h_max_reusable_secs,omitempty"`
        HKeepAlivePeriod int32            `json:"h_keep_alive_period,omitempty"`
    }

    type V2RayXHTTPOptions struct {
        Mode    string               `json:"mode,omitempty"`
        Host    string               `json:"host,omitempty"`
        Path    string               `json:"path,omitempty"`
        Method  string               `json:"method,omitempty"`
        Headers badoption.HTTPHeader `json:"headers,omitempty"`

        NoGRPCHeader bool `json:"no_grpc_header,omitempty"`
        NoSSEHeader  bool `json:"no_sse_header,omitempty"`

        XPaddingBytes        *V2RayXHTTPRange `json:"x_padding_bytes,omitempty"`
        ScMaxEachPostBytes   *V2RayXHTTPRange `json:"sc_max_each_post_bytes,omitempty"`
        ScMaxBufferedPosts   int32            `json:"sc_max_buffered_posts,omitempty"`
        ScMinPostsIntervalMs *V2RayXHTTPRange `json:"sc_min_posts_interval_ms,omitempty"`
        ScStreamUpServerSecs *V2RayXHTTPRange `json:"sc_stream_up_server_secs,omitempty"`

        XPaddingObfsMode  bool   `json:"x_padding_obfs_mode,omitempty"`
        XPaddingPlacement string `json:"x_padding_placement,omitempty"`
        XPaddingKey       string `json:"x_padding_key,omitempty"`
        XPaddingHeader    string `json:"x_padding_header,omitempty"`
        XPaddingMethod    string `json:"x_padding_method,omitempty"`

        SessionPlacement string `json:"session_placement,omitempty"`
        SessionKey       string `json:"session_key,omitempty"`
        SeqPlacement     string `json:"seq_placement,omitempty"`
        SeqKey           string `json:"seq_key,omitempty"`

        UplinkDataPlacement string           `json:"uplink_data_placement,omitempty"`
        UplinkDataKey       string           `json:"uplink_data_key,omitempty"`
        UplinkChunkSize     *V2RayXHTTPRange `json:"uplink_chunk_size,omitempty"`

        SessionIDTable  string           `json:"session_id_table,omitempty"`
        SessionIDLength *V2RayXHTTPRange `json:"session_id_length,omitempty"`

        ServerMaxHeaderBytes int32 `json:"server_max_header_bytes,omitempty"`

        DownloadSettings *V2RayXHTTPDownloadConfig `json:"download_settings,omitempty"`
        Xmux             *V2RayXHTTPXmuxConfig     `json:"xmux,omitempty"`
    }

    '''
).lstrip()

replace_exact(
    "option/v2ray_transport.go",
    marker,
    mirror + marker,
)

# ----------------------------------------------------------------------------
# 4. transport/v2ray/transport.go
# ----------------------------------------------------------------------------

replace_exact(
    "transport/v2ray/transport.go",
    '\tcase C.V2RayTransportTypeHTTPUpgrade:\n'
    '\t\treturn v2rayhttpupgrade.NewServer(ctx, logger, options.HTTPUpgradeOptions, tlsConfig, handler)\n'
    '\tdefault:\n',
    '\tcase C.V2RayTransportTypeHTTPUpgrade:\n'
    '\t\treturn v2rayhttpupgrade.NewServer(ctx, logger, options.HTTPUpgradeOptions, tlsConfig, handler)\n'
    '\tcase C.V2RayTransportTypeXHTTP:\n'
    '\t\treturn NewXHTTPServer(ctx, logger, options.XHTTPOptions, tlsConfig, handler)\n'
    '\tdefault:\n',
)

replace_exact(
    "transport/v2ray/transport.go",
    '\tcase C.V2RayTransportTypeHTTPUpgrade:\n'
    '\t\treturn v2rayhttpupgrade.NewClient(ctx, dialer, serverAddr, options.HTTPUpgradeOptions, tlsConfig)\n'
    '\tdefault:\n',
    '\tcase C.V2RayTransportTypeHTTPUpgrade:\n'
    '\t\treturn v2rayhttpupgrade.NewClient(ctx, dialer, serverAddr, options.HTTPUpgradeOptions, tlsConfig)\n'
    '\tcase C.V2RayTransportTypeXHTTP:\n'
    '\t\treturn NewXHTTPClient(ctx, dialer, serverAddr, options.XHTTPOptions, tlsConfig)\n'
    '\tdefault:\n',
)

print("xhttp applied.")
