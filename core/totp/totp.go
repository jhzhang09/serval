// Package totp 实现 RFC 6238 的基于时间的一次性密码。
//
// 只依赖标准库。密钥按 Base32 保存（和 otpauth 链接、常见验证器一致）。
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Params 是生成验证码需要的参数。零值 Digits、Period、Algorithm 分别表示 6 位、30 秒、SHA1。
type Params struct {
	// Secret 是不带填充的 Base32 密钥。
	Secret    string
	Digits    int
	Period    int
	Algorithm string
}

// Parse 接受 otpauth://totp/ 链接，或单独的 Base32 密钥。
// 不接受 HOTP：没有计数器就无法和服务器对齐。
func Parse(input string) (Params, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Params{}, fmt.Errorf("请粘贴 otpauth:// 链接或验证码密钥")
	}
	if strings.Contains(strings.ToLower(input), "otpauth://") {
		return parseURL(input)
	}
	secret, err := normalizeSecret(input)
	if err != nil {
		return Params{}, err
	}
	return Params{Secret: secret}, nil
}

func parseURL(input string) (Params, error) {
	u, err := url.Parse(input)
	if err != nil || !strings.EqualFold(u.Scheme, "otpauth") {
		return Params{}, fmt.Errorf("这不是 otpauth 链接，请粘贴验证器里的 otpauth://totp/ 链接")
	}
	if strings.EqualFold(u.Host, "hotp") {
		return Params{}, fmt.Errorf("只支持基于时间的验证码（otpauth://totp/），不支持计数器方式")
	}
	if !strings.EqualFold(u.Host, "totp") {
		return Params{}, fmt.Errorf("只支持基于时间的验证码（otpauth://totp/）")
	}
	q := u.Query()
	secret, err := normalizeSecret(q.Get("secret"))
	if err != nil {
		return Params{}, fmt.Errorf("链接里的验证码密钥无效")
	}
	p := Params{Secret: secret}
	if raw := q.Get("digits"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || (n != 6 && n != 8) {
			return Params{}, fmt.Errorf("验证码位数只支持 6 或 8")
		}
		if n != 6 {
			p.Digits = n
		}
	}
	if raw := q.Get("period"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 300 {
			return Params{}, fmt.Errorf("验证码周期必须是 1 到 300 秒")
		}
		if n != 30 {
			p.Period = n
		}
	}
	switch alg := strings.ToUpper(q.Get("algorithm")); alg {
	case "", "SHA1":
	case "SHA256", "SHA512":
		p.Algorithm = alg
	default:
		return Params{}, fmt.Errorf("不支持的验证码算法 %s", alg)
	}
	return p, nil
}

// Code 按 RFC 6238 计算 now 所在时间窗的验证码。
func Code(p Params, now time.Time) (string, error) {
	raw, err := decodeSecret(p.Secret)
	if err != nil {
		return "", err
	}
	return codeRaw(raw, p.normalized(), now)
}

// CodeRaw 用已经解码的密钥计算验证码。测试向量的种子不是 Base32，走这里。
func CodeRaw(secret []byte, digits, period int, algorithm string, now time.Time) (string, error) {
	if len(secret) == 0 {
		return "", fmt.Errorf("验证码密钥是空的")
	}
	return codeRaw(secret, (Params{Digits: digits, Period: period, Algorithm: algorithm}).normalized(), now)
}

func (p Params) normalized() Params {
	if p.Digits == 0 {
		p.Digits = 6
	}
	if p.Period == 0 {
		p.Period = 30
	}
	if p.Algorithm == "" {
		p.Algorithm = "SHA1"
	}
	return p
}

func codeRaw(secret []byte, p Params, now time.Time) (string, error) {
	h := hashFor(p.Algorithm)
	if h == nil {
		return "", fmt.Errorf("不支持的验证码算法")
	}
	counter := uint64(now.Unix()) / uint64(p.Period)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(h, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 0x0f)
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for range p.Digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", p.Digits, bin%mod), nil
}

func hashFor(algorithm string) func() hash.Hash {
	switch strings.ToUpper(algorithm) {
	case "SHA1":
		return sha1.New
	case "SHA256":
		return sha256.New
	case "SHA512":
		return sha512.New
	default:
		return nil
	}
}

func normalizeSecret(secret string) (string, error) {
	raw, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return enc.EncodeToString(raw), nil
}

func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.TrimSpace(secret))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.TrimRight(s, "=")
	if s == "" {
		return nil, fmt.Errorf("请粘贴 otpauth:// 链接或验证码密钥")
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("验证码密钥不是有效的 Base32")
	}
	return raw, nil
}
