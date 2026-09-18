// Package netx 统一管理进程级网络代理设置。
//
// 背景：服务化后（launchd/systemd/Windows 服务）进程不会继承 shell 的代理
// 环境变量，导致直连外网（如 api.telegram.org）超时。本包在启动早期根据
// 配置显式应用代理，使以下客户端均走代理：
//   - 依赖 http.DefaultTransport 的三方库（如 tgbotapi）
//   - 通过 ProxyFunc() 显式接入的自定义 Transport（如 llm 包）
package netx

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
)

var (
	mu       sync.RWMutex
	proxyURL *url.URL
)

// Setup 根据配置初始化全局代理。proxy 为空时不启用（保持默认行为）。
func Setup(proxy string) error {
	if proxy == "" {
		return nil
	}

	u, err := url.Parse(proxy)
	if err != nil {
		return fmt.Errorf("解析代理地址失败 %q: %w", proxy, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("代理地址格式不正确（需形如 http://127.0.0.1:1088）: %q", proxy)
	}

	mu.Lock()
	proxyURL = u
	mu.Unlock()

	// 1) 环境变量：兼容依赖 http.DefaultTransport / ProxyFromEnvironment 的三方库
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		_ = os.Setenv(k, proxy)
	}

	// 2) 显式覆写默认 Transport，规避 ProxyFromEnvironment 的缓存问题
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.Proxy = http.ProxyURL(u)
	}

	return nil
}

// ProxyFunc 返回可用于自定义 http.Transport 的 Proxy 函数。
// 未配置代理时回退到环境变量默认行为。
func ProxyFunc() func(*http.Request) (*url.URL, error) {
	mu.RLock()
	u := proxyURL
	mu.RUnlock()
	if u != nil {
		return http.ProxyURL(u)
	}
	return http.ProxyFromEnvironment
}

// Enabled 返回是否已启用显式代理。
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return proxyURL != nil
}
