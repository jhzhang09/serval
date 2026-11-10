package syncer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

var defaultHTTPClient = &http.Client{
	Timeout: 35 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// 彻底禁用 HTTP/2，强制使用成熟稳定的 HTTP/1.1。
		// 避免 Cloudflare / AList 代理云盘时频繁发生 HTTP/2 stream error (INTERNAL_ERROR) 与 502 网关重置。
		ForceAttemptHTTP2: false,
		TLSNextProto:      make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

const maxVaultBytes = 32 << 20

var (
	// ErrNotFound 表示远端还没有 vault.json。
	ErrNotFound = errors.New("远端还没有保险库文件")
	// ErrConflict 表示上传时 ETag 已经对不上，需要重新下载合并。
	ErrConflict = errors.New("远端文件已被其他设备更新")
	// ErrAuth 表示服务器拒绝了账号或密码。
	ErrAuth = errors.New("同步账号或密码不正确")
	// ErrUnavailable 表示请求没有完成（断网、超时、地址不对）。
	ErrUnavailable = errors.New("连不上同步服务")
	// ErrNoFolder 表示 vault.json 的上一级目录不存在。
	ErrNoFolder = errors.New("同步目录不存在")
	// ErrNoETag 表示服务器没有给出文件版本，继续上传可能覆盖别人刚写的内容。
	ErrNoETag = errors.New("同步服务没有返回文件版本")
)

// Client 用 HTTP 访问一份 WebDAV 上的 vault.json。只使用 GET、PUT 和 PROPFIND。
type Client struct {
	// URL 是 vault.json 的完整地址。
	URL      string
	User     string
	Password string
	// HTTP 为空时用 http.DefaultClient。超时由请求的 context 控制。
	HTTP *http.Client
}

// FileURLWithDir 把用户填写的服务器根地址与远程子目录规范成完整的 vault.json URL。
// 如果 subDir 为空，则保存在服务器根目录；非空则保存在指定子目录下。
func FileURLWithDir(rawServerURL, subDir string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawServerURL))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("服务器地址需要以 http:// 或 https:// 开头")
	}
	if u.User != nil {
		return "", errors.New("不要把账号和密码写在地址里")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("服务器地址里不能带查询参数")
	}

	cleanSub := strings.Trim(strings.TrimSpace(subDir), "/")
	basePath := strings.TrimRight(u.Path, "/")
	if strings.EqualFold(path.Base(basePath), "vault.json") {
		// 用户直接填了完整 vault.json 地址
		return u.String(), nil
	}

	if cleanSub == "" {
		u.Path = path.Clean(basePath + "/vault.json")
	} else {
		cleanSub = strings.TrimSuffix(cleanSub, "/vault.json")
		if cleanSub == "" {
			u.Path = path.Clean(basePath + "/vault.json")
		} else {
			u.Path = path.Clean(basePath + "/" + cleanSub + "/vault.json")
		}
	}
	return u.String(), nil
}

// FileURL 把用户填写的地址规范成 vault.json 的 URL。
// 目录地址会补上 /vault.json。账号和密码不能写在地址里。
func FileURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("同步地址需要以 http:// 或 https:// 开头")
	}
	if u.User != nil {
		return "", errors.New("不要把账号和密码写在地址里")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("同步地址里不能带查询参数")
	}
	if !strings.EqualFold(path.Base(u.Path), "vault.json") {
		if u.Path == "" || u.Path == "/" {
			u.Path = "/vault.json"
		} else {
			u.Path = strings.TrimRight(u.Path, "/") + "/vault.json"
		}
	}
	return u.String(), nil
}

// strongETag 保证 ETag 是去除 W/ 弱标记并且带有双引号的强验证器。
// Cloudflare 或某些代理在开启压缩时会把 ETag 弱化（加上 W/），而在 If-Match 中使用弱 ETag 会导致服务器返回 412。
func strongETag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "W/")
	s = strings.TrimPrefix(s, "w/")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, `"`) {
		s = `"` + s + `"`
	}
	return s
}

// Probe 检查账号能否访问这个地址。文件还不存在也算连上了，因为新设备第一次同步时远端就是空的。
func (c *Client) Probe(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			c.closeIdle()
		}
		if err := retryBackoff(ctx, attempt); err != nil {
			return err
		}
		resp, err := c.do(ctx, http.MethodGet, nil, nil)
		if err != nil {
			lastErr = err
			if isTransientErr(err) {
				continue
			}
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK, http.StatusNotFound:
			return nil
		default:
			if isTransientHTTPStatus(resp.StatusCode) {
				lastErr = fmt.Errorf("%w: 测试连接时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
				continue
			}
			if err := classify(resp.StatusCode); err != nil {
				return err
			}
			return fmt.Errorf("%w: 测试连接时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return ErrUnavailable
}

// Download 读取远端文件。404 返回 ErrNotFound。
func (c *Client) Download(ctx context.Context) ([]byte, string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			c.closeIdle()
		}
		if err := retryBackoff(ctx, attempt); err != nil {
			return nil, "", err
		}
		resp, err := c.do(ctx, http.MethodGet, nil, nil)
		if err != nil {
			lastErr = err
			if isTransientErr(err) {
				continue
			}
			return nil, "", err
		}

		if resp.StatusCode == http.StatusNotFound {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, "", ErrNotFound
		}
		if isTransientHTTPStatus(resp.StatusCode) {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("%w: 读取保险库时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
			continue
		}
		if err := classify(resp.StatusCode); err != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, "", err
		}
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, "", fmt.Errorf("%w: 读取保险库时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
		}

		body, err := readLimited(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			if isTransientErr(err) {
				continue
			}
			return nil, "", err
		}

		etag := strongETag(resp.Header.Get("ETag"))
		if etag == "" {
			raw, err := c.propfindETag(ctx)
			if err != nil {
				lastErr = err
				continue
			}
			etag = strongETag(raw)
		}
		if etag == "" {
			return nil, "", ErrNoETag
		}
		return body, etag, nil
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", ErrUnavailable
}

// Upload 上传保险库。ifMatch 为空时要求远端还没有文件。
func (c *Client) Upload(ctx context.Context, body []byte, ifMatch string) (string, error) {
	headers := map[string]string{"Content-Type": "application/json"}
	match := strongETag(ifMatch)
	if match == "" {
		headers["If-None-Match"] = "*"
	} else {
		headers["If-Match"] = match
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			c.closeIdle()
		}
		if err := retryBackoff(ctx, attempt); err != nil {
			return "", err
		}
		resp, err := c.do(ctx, http.MethodPut, bytes.NewReader(body), headers)
		if err != nil {
			lastErr = err
			if isTransientErr(err) {
				continue
			}
			return "", err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK, http.StatusCreated, http.StatusNoContent:
			return strongETag(resp.Header.Get("ETag")), nil
		case http.StatusPreconditionFailed:
			return "", ErrConflict
		case http.StatusConflict:
			// 409 Conflict 通常表示父级目录不存在，尝试自动创建父目录并重试
			if c.ensureParentDir(ctx) == nil {
				continue
			}
			return "", ErrNoFolder
		default:
			if isTransientHTTPStatus(resp.StatusCode) {
				lastErr = fmt.Errorf("%w: 上传保险库时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
				continue
			}
			if err := classify(resp.StatusCode); err != nil {
				return "", err
			}
			return "", fmt.Errorf("%w: 上传保险库时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", ErrUnavailable
}

func (c *Client) propfindETag(ctx context.Context) (string, error) {
	const body = `<?xml version="1.0" encoding="utf-8" ?><D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			c.closeIdle()
		}
		if err := retryBackoff(ctx, attempt); err != nil {
			return "", err
		}
		resp, err := c.do(ctx, "PROPFIND", strings.NewReader(body), map[string]string{
			"Depth":        "0",
			"Content-Type": "application/xml",
		})
		if err != nil {
			lastErr = err
			if isTransientErr(err) {
				continue
			}
			return "", err
		}
		if isTransientHTTPStatus(resp.StatusCode) {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("%w: 查询文件信息时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return "", nil
		}
		payload, err := readLimited(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return etagFromPropfind(payload), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", nil
}

func etagFromPropfind(payload []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(payload))
	want := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			want = t.Name.Local == "getetag"
		case xml.EndElement:
			if t.Name.Local == "getetag" {
				want = false
			}
		case xml.CharData:
			if want {
				if s := strings.TrimSpace(string(t)); s != "" {
					return s
				}
			}
		}
	}
}

func (c *Client) closeIdle() {
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTPClient
	}
	if tr, ok := hc.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

func (c *Client) do(ctx context.Context, method string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, "同步地址无法使用")
	}
	if c.User != "" || c.Password != "" {
		req.SetBasicAuth(c.User, c.Password)
	}
	// 避免反向代理或网盘服务识别到 Go-http-client 从而掐断流或限流
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Serval/0.1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTPClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: 连接超时", ErrUnavailable)
		}
		return nil, fmt.Errorf("%w: 请求没有完成 (%v)", ErrUnavailable, err)
	}
	return resp, nil
}

func classify(code int) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusConflict:
		return ErrNoFolder
	default:
		return nil
	}
}

func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxVaultBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 没有读完响应 (%v)", ErrUnavailable, err)
	}
	if len(b) > maxVaultBytes {
		return nil, errors.New("远端的保险库文件过大，已中止同步")
	}
	return b, nil
}

func isTransientHTTPStatus(code int) bool {
	switch code {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusTooManyRequests,
		520, 521, 522, 523, 524: // 常见反向代理或网盘服务瞬态故障码
		return true
	default:
		return false
	}
}

func isTransientErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true
}

func retryBackoff(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	// 网盘服务在写入后有短暂落盘延迟，且 502 冷启动需要退避时间
	delay := time.Duration(attempt) * 1000 * time.Millisecond
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func (c *Client) ensureParentDir(ctx context.Context) error {
	u, err := url.Parse(c.URL)
	if err != nil {
		return err
	}
	parent := path.Dir(strings.TrimRight(u.Path, "/"))
	if parent == "/" || parent == "." || parent == "" {
		return nil
	}
	u.Path = parent + "/"
	req, err := http.NewRequestWithContext(ctx, "MKCOL", u.String(), nil)
	if err != nil {
		return err
	}
	if c.User != "" || c.Password != "" {
		req.SetBasicAuth(c.User, c.Password)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Serval/0.1.0")
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTPClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("mkdir status: %d", resp.StatusCode)
}
