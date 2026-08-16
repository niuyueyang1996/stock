package raw

import "net/http"

// 测试钩子：供其它包（如 internal/service/indices）测试注入 mock HTTP 传输，
// 以驱动腾讯/乐咕等数据源返回固定响应。生产调用链不使用这些方法，仅测试使用。
// 原理与同包 raw_test.go 的 attachTransport 一致（替换 http.Client.Transport）。

// AttachTestTransport 替换 Tencent 底层 http.RoundTripper（仅测试）。
func (t *Tencent) AttachTestTransport(fn func(*http.Request) (*http.Response, error)) {
	t.c.http.Transport = roundTripFunc(fn)
}

// AttachTestTransport 替换 EM 内各子客户端底层 http.RoundTripper（仅测试）。
// datacenter / fflow-kline 共用同一 fn，按请求 URL 路由（clist / 天天基金页）。
func (e *EM) AttachTestTransport(fn func(*http.Request) (*http.Response, error)) {
	for _, c := range []*Client{e.dc, e.ff, e.kln} {
		c.http.Transport = roundTripFunc(fn)
	}
}

// AttachTestTransport 替换 Sina 底层 http.RoundTripper（仅测试）。
func (s *Sina) AttachTestTransport(fn func(*http.Request) (*http.Response, error)) {
	s.c.http.Transport = roundTripFunc(fn)
}

// AttachTestTransport 替换 Legu 底层 http.RoundTripper（仅测试）。
func (l *Legu) AttachTestTransport(fn func(*http.Request) (*http.Response, error)) {
	l.c.http.Transport = roundTripFunc(fn)
}
