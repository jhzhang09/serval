package syncer

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

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

// Download 读取远端文件。404 返回 ErrNotFound。
func (c *Client) Download(ctx context.Context) ([]byte, string, error) {
	resp, err := c.do(ctx, http.MethodGet, nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body)
		return nil, "", ErrNotFound
	}
	if err := classify(resp.StatusCode); err != nil {
		io.Copy(io.Discard, resp.Body)
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, "", fmt.Errorf("%w: 读取保险库时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
	}
	body, err := readLimited(resp.Body)
	if err != nil {
		return nil, "", err
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		etag, err = c.propfindETag(ctx)
		if err != nil {
			return nil, "", err
		}
	}
	if etag == "" {
		return nil, "", ErrNoETag
	}
	return body, etag, nil
}

// Upload 上传保险库。ifMatch 为空时要求远端还没有文件。
func (c *Client) Upload(ctx context.Context, body []byte, ifMatch string) (string, error) {
	headers := map[string]string{"Content-Type": "application/json"}
	if ifMatch == "" {
		headers["If-None-Match"] = "*"
	} else {
		headers["If-Match"] = ifMatch
	}
	resp, err := c.do(ctx, http.MethodPut, bytes.NewReader(body), headers)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return resp.Header.Get("ETag"), nil
	case http.StatusPreconditionFailed:
		return "", ErrConflict
	default:
		if err := classify(resp.StatusCode); err != nil {
			return "", err
		}
		return "", fmt.Errorf("%w: 上传保险库时服务器返回了 %d", ErrUnavailable, resp.StatusCode)
	}
}

func (c *Client) propfindETag(ctx context.Context) (string, error) {
	const body = `<?xml version="1.0" encoding="utf-8" ?><D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`
	resp, err := c.do(ctx, "PROPFIND", strings.NewReader(body), map[string]string{
		"Depth":        "0",
		"Content-Type": "application/xml",
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return "", nil
	}
	payload, err := readLimited(resp.Body)
	if err != nil {
		return "", err
	}
	return etagFromPropfind(payload), nil
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

func (c *Client) do(ctx context.Context, method string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, "同步地址无法使用")
	}
	if c.User != "" || c.Password != "" {
		req.SetBasicAuth(c.User, c.Password)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, "请求没有完成")
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
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, "没有读完响应")
	}
	if len(b) > maxVaultBytes {
		return nil, errors.New("远端的保险库文件过大，已中止同步")
	}
	return b, nil
}
