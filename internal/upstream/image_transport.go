package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// newImageHTTPClient 返回用于向上游图片地址发起请求的 HTTP 客户端：
// 拨号时解析域名并只连接公网地址，不跟随重定向。
func newImageHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		// Direct connections ensure the validated IP is also the actual target.
		Proxy:                 nil,
		DialContext:           imageDialContext(dialer),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

var errImageTargetForbidden = errors.New("image target resolves to a forbidden address")

func imageDialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error = errImageTargetForbidden
		for _, ip := range ips {
			if !isPublicImageIP(ip.IP) {
				continue
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

func isPublicImageIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	// 非公网特殊用途地址：CGNAT、基准测试、文档示例与保留网段。
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 0 || v4[0] >= 240 ||
			(v4[0] == 100 && v4[1]&0xc0 == 64) ||
			(v4[0] == 198 && (v4[1]&0xfe == 18 || (v4[1] == 51 && v4[2] == 100))) ||
			(v4[0] == 192 && v4[1] == 0 && (v4[2] == 0 || v4[2] == 2)) ||
			(v4[0] == 203 && v4[1] == 0 && v4[2] == 113) {
			return false
		}
	} else if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 {
		return false
	}
	return true
}
