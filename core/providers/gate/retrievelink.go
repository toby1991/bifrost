package gate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/network"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// retrieveLinkMaxBudget 是单次直链查询的硬预算上限。
const retrieveLinkMaxBudget = 2 * time.Second

// retrieveLinkDeadlineReserve 是必须给外层 context 调用方保留的剩余时间余量；
// 外层剩余不足该余量时直链查询整体跳过。
const retrieveLinkDeadlineReserve = 100 * time.Millisecond

// retrieveLinkHopByHopHeaders 是 HTTP/1.1 逐跳头集合（providerUtils 内部的
// hopByHopHeaders 不导出，这里保留一份拷贝）；context 透传头里的逐跳头必须排除。
var retrieveLinkHopByHopHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// newRetrieveLinkClient 构造 VideoRetrieve 直链查询专用的私有 http.Client。
// 与 provider 主 client（共享 fasthttp 连接池）完全隔离：DisableKeepAlives
// 让每次取链使用全新连接、用后关闭，未读的 302 正文绝不会污染任何复用
// 连接；CheckRedirect 永不跟随重定向。返回 error 表示配置级 fail-closed
// （如 CA/proxy secret 引用解析为空），调用方存下后让取链永远失败。
func newRetrieveLinkClient(networkConfig schemas.NetworkConfig, proxyConfig *schemas.ProxyConfig, logger schemas.Logger) (*http.Client, error) {
	proxyFunc, err := newRetrieveLinkProxyFunc(proxyConfig, logger)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := newRetrieveLinkTLSConfig(networkConfig, proxyConfig, logger)
	if err != nil {
		return nil, err
	}

	// 配置代理时目标代理由代理解析（与 fasthttp 路径一致），直接拨号；
	// 否则拨号前对目标 IP 做网络准入校验。
	dialContext := (&net.Dialer{}).DialContext
	if proxyFunc == nil {
		dialContext = newRetrieveLinkDialContext(networkConfig.AllowPrivateNetwork)
	}

	transport := &http.Transport{
		Proxy:             proxyFunc,
		DialContext:       dialContext,
		TLSClientConfig:   tlsConfig,
		DisableKeepAlives: true,
	}
	return &http.Client{
		Transport: transport,
		// 只读第一跳响应头：绝不跟随重定向，绝不接触 CDN。
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// newRetrieveLinkProxyFunc 把 schemas.ProxyConfig 映射为 http.Transport.Proxy，
// 镜像 providerUtils.ConfigureProxy 的凭证注入与 warn 语义；secret 引用解析
// 为空返回错误让取链 fail-closed。返回 nil proxyFunc 表示不走代理。
func newRetrieveLinkProxyFunc(proxyConfig *schemas.ProxyConfig, logger schemas.Logger) (func(*http.Request) (*url.URL, error), error) {
	if proxyConfig == nil {
		return nil, nil
	}
	switch proxyConfig.Type {
	case schemas.NoProxy:
		return nil, nil
	case schemas.HTTPProxy, schemas.Socks5Proxy:
		if proxyConfig.URL.IsFromSecret() && proxyConfig.URL.GetValue() == "" {
			errMsg := fmt.Sprintf("invalid proxy configuration: %s references %q but it resolved to an empty value", "proxy.url", proxyConfig.URL.GetRawRef())
			logger.Error(errMsg)
			return nil, errors.New(errMsg)
		}
		proxyURLValue := proxyConfig.URL.GetValue()
		if proxyURLValue == "" {
			if proxyConfig.Type == schemas.Socks5Proxy {
				logger.Warn("Warning: SOCKS5 proxy URL is required for setting up proxy")
			} else {
				logger.Warn("Warning: HTTP proxy URL is required for setting up proxy")
			}
			return nil, nil
		}
		// net/http 要求带 scheme 的 proxy URL；只配置 host:port 时按类型补全
		// （SOCKS5 走 socks5:// scheme，由 net/http 原生支持）。
		if !strings.Contains(proxyURLValue, "://") {
			if proxyConfig.Type == schemas.Socks5Proxy {
				proxyURLValue = "socks5://" + proxyURLValue
			} else {
				proxyURLValue = "http://" + proxyURLValue
			}
		}
		parsedURL, err := url.Parse(proxyURLValue)
		if err != nil {
			if proxyConfig.Type == schemas.Socks5Proxy {
				logger.Warn("Invalid proxy configuration: invalid SOCKS5 proxy URL")
			} else {
				logger.Warn("Invalid proxy configuration: invalid HTTP proxy URL")
			}
			return nil, nil
		}
		// 与 ConfigureProxy 一致：仅当用户名密码同时提供时注入凭证。
		if username, password := proxyConfig.Username.GetValue(), proxyConfig.Password.GetValue(); username != "" && password != "" {
			parsedURL.User = url.UserPassword(username, password)
		}
		return http.ProxyURL(parsedURL), nil
	case schemas.EnvProxy:
		return http.ProxyFromEnvironment, nil
	default:
		logger.Warn("Invalid proxy configuration: unsupported proxy type: %s", proxyConfig.Type)
		return nil, nil
	}
}

// newRetrieveLinkTLSConfig 为取链 client 构造 TLS 配置，规则与 provider 主
// client（providerUtils.ConfigureProxy + ConfigureTLS）一致：proxy CA 先并入
// 系统根池，network CA 再合并；MinVersion 固定 TLS1.2。任一 CA secret 引用
// 解析为空即返回错误让取链 fail-closed。
func newRetrieveLinkTLSConfig(networkConfig schemas.NetworkConfig, proxyConfig *schemas.ProxyConfig, logger schemas.Logger) (*tls.Config, error) {
	var tlsConfig *tls.Config

	// proxy CA（与 ConfigureProxy 同语义）
	if proxyConfig != nil {
		if proxyConfig.CACertPEM.IsFromSecret() && proxyConfig.CACertPEM.GetValue() == "" {
			errMsg := fmt.Sprintf("invalid proxy configuration: %s references %q but it resolved to an empty value", "proxy.ca_cert_pem", proxyConfig.CACertPEM.GetRawRef())
			logger.Error(errMsg)
			return nil, errors.New(errMsg)
		}
		if proxyCACertPEM := proxyConfig.CACertPEM.GetValue(); proxyCACertPEM != "" {
			proxyTLSConfig, err := createRetrieveLinkTLSConfigWithCA(proxyCACertPEM)
			if err != nil {
				logger.Warn("Failed to configure custom CA certificate: %v", err)
			} else {
				tlsConfig = proxyTLSConfig
			}
		}
	}

	// network CA + InsecureSkipVerify（与 ConfigureTLS 同语义）
	if networkConfig.CACertPEM.IsFromSecret() && networkConfig.CACertPEM.GetValue() == "" {
		errMsg := fmt.Sprintf("invalid provider configuration: %s references %q but it resolved to an empty value", "network_config.ca_cert_pem", networkConfig.CACertPEM.GetRawRef())
		logger.Error(errMsg)
		return nil, errors.New(errMsg)
	}
	caCertPEM := networkConfig.CACertPEM.GetValue()
	if !networkConfig.InsecureSkipVerify && caCertPEM == "" {
		return tlsConfig, nil
	}
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if networkConfig.InsecureSkipVerify {
		logger.Warn("insecure_skip_verify is enabled for provider — TLS certificate verification is disabled. Not recommended for production.")
		tlsConfig.InsecureSkipVerify = true
	}
	if caCertPEM != "" {
		caTLSConfig, err := createRetrieveLinkTLSConfigWithCA(caCertPEM)
		if err != nil {
			logger.Warn("Failed to configure custom CA certificate for provider: %v", err)
		} else if tlsConfig.RootCAs != nil {
			// 合并：把 network CA 追加进既有根池（如 proxy CA）。
			if !tlsConfig.RootCAs.AppendCertsFromPEM([]byte(caCertPEM)) {
				logger.Warn("Failed to append CA certificate to existing TLS config")
			}
		} else {
			tlsConfig.RootCAs = caTLSConfig.RootCAs
		}
	}
	return tlsConfig, nil
}

// createRetrieveLinkTLSConfigWithCA 与 providerUtils.createTLSConfigWithCA 同
// 语义（该 helper 不导出，这里保留一份拷贝）：把自定义 CA 追加进系统根池。
func createRetrieveLinkTLSConfigWithCA(caCertPEM string) (*tls.Config, error) {
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		// 拿不到系统根池时退化为全新池
		rootCAs = x509.NewCertPool()
	}
	if !rootCAs.AppendCertsFromPEM([]byte(caCertPEM)) {
		return nil, fmt.Errorf("failed to parse CA certificate PEM")
	}
	return &tls.Config{
		RootCAs:    rootCAs,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// newRetrieveLinkDialContext 返回取链 transport 的 DialContext（仅无代理时
// 使用），与 providerUtils.configureDialer 默认分支同一套网络准入规则：
// 先用单次取链的 context 解析目标 host，unspecified/link-local 永远拒绝，
// private 需 AllowPrivateNetwork（loopback 永远放行），随后直接拨已校验的
// IP 字面量，关闭 DNS rebinding 窗口。
func newRetrieveLinkDialContext(allowPrivateNetwork bool) func(ctx context.Context, networkName, addr string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, networkName, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		var conn net.Conn
		var lastErr error
		for _, ip := range ips {
			// Unspecified（0.0.0.0, ::）与 link-local（169.254.x.x, fe80::）永远拒绝
			if ip.IsUnspecified() {
				return nil, fmt.Errorf("connection to unspecified IP %s is not allowed", ip)
			}
			if network.IsLinkLocal(ip) {
				return nil, fmt.Errorf("connection to link-local IP %s is not allowed", ip)
			}
			// RFC 1918 需 operator 显式放行；loopback 永远允许
			if !ip.IsLoopback() && !allowPrivateNetwork && network.IsPrivateIP(ip) {
				return nil, fmt.Errorf("connection to private IP %s is not allowed", ip)
			}
			conn, err = dialer.DialContext(ctx, networkName, net.JoinHostPort(ip.String(), port))
			if err == nil {
				break
			}
			lastErr = err
		}
		if conn == nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("no usable address resolved for %s", host)
		}
		return conn, nil
	}
}

// setRetrieveLinkExtraHeaders 镜像 providerUtils.SetExtraHeaders 语义：先写
// networkConfig.ExtraHeaders（已存在则不覆盖），context 里的
// BifrostContextKeyExtraHeaders 优先并排除逐跳头。
func setRetrieveLinkExtraHeaders(ctx *schemas.BifrostContext, req *http.Request, extraHeaders map[string]string) {
	for key, value := range extraHeaders {
		canonicalKey := textproto.CanonicalMIMEHeaderKey(key)
		// 已存在的头不覆盖，避免改写重要头
		if req.Header.Get(canonicalKey) == "" {
			req.Header.Set(canonicalKey, value)
		}
	}
	// context 里的 extra headers 优先
	if ctxHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
		for k, values := range ctxHeaders {
			if retrieveLinkHopByHopHeaders[strings.ToLower(k)] {
				continue
			}
			for i, v := range values {
				if i == 0 {
					req.Header.Set(k, v)
				} else {
					req.Header.Add(k, v)
				}
			}
		}
	}
}
