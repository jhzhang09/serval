package syncer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

// S3Client 通过 S3 兼容协议（支持 AWS S3、Cloudflare R2、MinIO 等）访问远端 vault.json。
// 仅实现 GET、PUT、HEAD，使用标准库计算 AWS Signature Version 4 签名，零额外依赖。
type S3Client struct {
	// Endpoint 为 S3 服务的完整基础地址（如 https://s3.us-east-1.amazonaws.com，或 https://<account>.r2.cloudflarestorage.com）。
	// 可以包含协议与可选端口。如果为空，默认使用 https://s3.amazonaws.com。
	Endpoint string
	// Region 是存储桶所在的区域，如 us-east-1 或 auto（Cloudflare R2 常用）。
	Region string
	// Bucket 是存储桶名称。
	Bucket string
	// Key 是存储桶中的对象键名，默认或规范化后以 vault.json 结尾。
	Key string
	// AccessKeyId 与 SecretAccessKey 是访问密钥。
	AccessKeyId     string
	SecretAccessKey string

	// HTTP 为空时使用 http.DefaultClient。
	HTTP *http.Client
	// Now 提供当前时间注入，测试时可固定时间戳。为空时使用 time.Now。
	Now func() time.Time
}

// S3URL 解析类似 s3://bucket/path/vault.json 或 https://endpoint/bucket/path/vault.json 格式的 URL。
func ParseS3Target(raw string) (bucket, key string, err error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", errors.New("存储桶或路径不能为空")
	}

	if strings.HasPrefix(s, "s3://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", "", errors.New("无效的 s3:// 地址格式，应形如 s3://my-bucket/serval/vault.json")
		}
		bucket = u.Host
		keyPath := strings.Trim(u.Path, "/")
		if keyPath == "" {
			key = "vault.json"
		} else if strings.EqualFold(path.Base(keyPath), "vault.json") {
			key = keyPath
		} else {
			key = keyPath + "/vault.json"
		}
		return bucket, key, nil
	}

	// 支持直接填路径或桶名
	parts := strings.SplitN(strings.Trim(s, "/"), "/", 2)
	bucket = parts[0]
	if bucket == "" {
		return "", "", errors.New("存储桶名称不能为空")
	}
	if len(parts) == 1 || parts[1] == "" {
		key = "vault.json"
	} else {
		keyPath := parts[1]
		if strings.EqualFold(path.Base(keyPath), "vault.json") {
			key = keyPath
		} else {
			key = keyPath + "/vault.json"
		}
	}
	return bucket, key, nil
}

// S3Target 表示规范化后的 S3 目标地址信息。
type S3Target struct {
	Endpoint string
	Region   string
	Bucket   string
	Key      string
}

// FormatURL 格式化为内部统一的 s3:// 地址。
// 例如：s3://my-bucket/serval/vault.json?endpoint=https%3A%2F%2Fcustom.endpoint&region=auto
func (t S3Target) FormatURL() string {
	b := strings.TrimSpace(t.Bucket)
	k := strings.Trim(t.Key, "/")
	if k == "" {
		k = "vault.json"
	}
	base := fmt.Sprintf("s3://%s/%s", b, k)

	vals := url.Values{}
	if ep := strings.TrimSpace(t.Endpoint); ep != "" {
		vals.Set("endpoint", ep)
	}
	if reg := strings.TrimSpace(t.Region); reg != "" {
		vals.Set("region", reg)
	}
	if len(vals) > 0 {
		return base + "?" + vals.Encode()
	}
	return base
}

// IsS3URL 判断 URL 是否为 S3 协议目标。
func IsS3URL(raw string) bool {
	s := strings.TrimSpace(raw)
	return strings.HasPrefix(s, "s3://")
}

// ParseS3URL 解析统一的 s3://... URL。
func ParseS3URL(raw string) (*S3Target, error) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "s3://") {
		return nil, errors.New("不是合法的 s3:// 地址")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, errors.New("无效的 s3:// 地址格式，应形如 s3://my-bucket/vault.json")
	}
	target := &S3Target{
		Bucket: u.Host,
	}
	keyPath := strings.Trim(u.Path, "/")
	if keyPath == "" {
		target.Key = "vault.json"
	} else if strings.EqualFold(path.Base(keyPath), "vault.json") {
		target.Key = keyPath
	} else {
		target.Key = keyPath + "/vault.json"
	}

	q := u.Query()
	target.Endpoint = q.Get("endpoint")
	target.Region = q.Get("region")
	return target, nil
}

// NewS3ClientFromURL 根据 s3:// 规范地址以及凭据构造 S3Client。
func NewS3ClientFromURL(s3URL, accessKey, secretKey string) (*S3Client, error) {
	target, err := ParseS3URL(s3URL)
	if err != nil {
		return nil, err
	}
	return &S3Client{
		Endpoint:        target.Endpoint,
		Region:          target.Region,
		Bucket:          target.Bucket,
		Key:             target.Key,
		AccessKeyId:     strings.TrimSpace(accessKey),
		SecretAccessKey: strings.TrimSpace(secretKey),
	}, nil
}

func (c *S3Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *S3Client) endpointURL() (*url.URL, error) {
	ep := strings.TrimSpace(c.Endpoint)
	if ep == "" {
		ep = "https://s3.amazonaws.com"
	}
	if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("无效的 S3 终端地址: %s", ep)
	}
	return u, nil
}

// objectURL 返回请求的具体 URL（路径样式 Path-Style：{endpoint}/{bucket}/{key}）。
func (c *S3Client) objectURL() (*url.URL, error) {
	u, err := c.endpointURL()
	if err != nil {
		return nil, err
	}
	b := strings.TrimSpace(c.Bucket)
	if b == "" {
		return nil, errors.New("缺少存储桶名称 (Bucket)")
	}
	k := strings.TrimSpace(c.Key)
	if k == "" {
		k = "vault.json"
	}
	k = strings.TrimPrefix(k, "/")

	p := path.Clean("/" + u.Path + "/" + b + "/" + k)
	u.Path = p
	return u, nil
}

// Probe 测试 S3 服务与认证可用性。
func (c *S3Client) Probe(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := retryBackoff(ctx, attempt); err != nil {
			return err
		}
		resp, err := c.do(ctx, http.MethodHead, nil, nil)
		if err != nil {
			lastErr = err
			if isTransientErr(err) {
				continue
			}
			return err
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK, http.StatusNotFound:
			return nil
		default:
			if isTransientHTTPStatus(resp.StatusCode) {
				lastErr = fmt.Errorf("%w: 测试 S3 连接时返回了 %d", ErrUnavailable, resp.StatusCode)
				continue
			}
			if err := classifyS3(resp.StatusCode); err != nil {
				return err
			}
			return fmt.Errorf("%w: 测试 S3 连接时返回了 %d", ErrUnavailable, resp.StatusCode)
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return ErrUnavailable
}

// Download 下载保险库文件。
func (c *S3Client) Download(ctx context.Context) ([]byte, string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
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
			lastErr = fmt.Errorf("%w: 下载保险库时返回了 %d", ErrUnavailable, resp.StatusCode)
			continue
		}
		if err := classifyS3(resp.StatusCode); err != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, "", err
		}
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, "", fmt.Errorf("%w: 下载保险库时返回了 %d", ErrUnavailable, resp.StatusCode)
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

		etag := cleanETag(resp.Header.Get("ETag"))
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

// Upload 上传保险库文件。
func (c *S3Client) Upload(ctx context.Context, body []byte, ifMatch string) (string, error) {
	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if ifMatch == "" {
		headers["If-None-Match"] = "*"
	} else {
		headers["If-Match"] = quoteETag(ifMatch)
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := retryBackoff(ctx, attempt); err != nil {
			return "", err
		}
		resp, err := c.do(ctx, http.MethodPut, body, headers)
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
			return cleanETag(resp.Header.Get("ETag")), nil
		case http.StatusPreconditionFailed:
			return "", ErrConflict
		default:
			if isTransientHTTPStatus(resp.StatusCode) {
				lastErr = fmt.Errorf("%w: 上传保险库时返回了 %d", ErrUnavailable, resp.StatusCode)
				continue
			}
			if err := classifyS3(resp.StatusCode); err != nil {
				return "", err
			}
			return "", fmt.Errorf("%w: 上传保险库时返回了 %d", ErrUnavailable, resp.StatusCode)
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", ErrUnavailable
}

func cleanETag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "W/")
	return strings.Trim(s, `"`)
}

func quoteETag(s string) string {
	s = strings.TrimSpace(s)
	if s == "*" {
		return "*"
	}
	if !strings.HasPrefix(s, `"`) {
		s = `"` + s + `"`
	}
	return s
}

func classifyS3(code int) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	default:
		return nil
	}
}

// do 执行带 SigV4 签名的 HTTP 请求。
func (c *S3Client) do(ctx context.Context, method string, payload []byte, extraHeaders map[string]string) (*http.Response, error) {
	reqURL, err := c.objectURL()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	var bodyReader io.Reader
	if len(payload) > 0 {
		bodyReader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("%w: 创建请求失败", ErrUnavailable)
	}

	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	// 计算 SigV4
	t := c.now()
	dateStamp := t.Format("20060102")
	amzDate := t.Format("20060102T150405Z")

	payloadHash := sha256Hex(payload)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Host", reqURL.Host)

	region := strings.TrimSpace(c.Region)
	if region == "" {
		region = "us-east-1"
	}
	service := "s3"

	// CanonicalHeaders & SignedHeaders
	headerKeys := make([]string, 0, len(req.Header))
	for k := range req.Header {
		headerKeys = append(headerKeys, strings.ToLower(k))
	}
	sort.Strings(headerKeys)

	var canonicalHeaders strings.Builder
	for _, k := range headerKeys {
		val := strings.TrimSpace(req.Header.Get(k))
		canonicalHeaders.WriteString(fmt.Sprintf("%s:%s\n", k, val))
	}
	signedHeaders := strings.Join(headerKeys, ";")

	// Canonical URI: 路径各段需转义
	canonicalURI := escapePath(reqURL.Path)

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		canonicalURI,
		"", // query string
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	)

	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, region, service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	)

	signingKey := getSignatureKey(c.SecretAccessKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKeyId,
		credentialScope,
		signedHeaders,
		signature,
	)
	req.Header.Set("Authorization", authHeader)

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return resp, nil
}

func escapePath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	res := strings.Join(segments, "/")
	if !strings.HasPrefix(res, "/") {
		res = "/" + res
	}
	return res
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func getSignatureKey(secret, dateStamp, regionName, serviceName string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(regionName))
	kService := hmacSHA256(kRegion, []byte(serviceName))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}
