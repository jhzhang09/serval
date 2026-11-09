package totp

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 附录 B 的测试向量。种子是 ASCII，不是 Base32。
func TestRFC6238Vectors(t *testing.T) {
	sha1 := []byte("12345678901234567890")
	sha256 := []byte("12345678901234567890123456789012")
	sha512 := []byte("1234567890123456789012345678901234567890123456789012345678901234")
	cases := []struct {
		at        int64
		algorithm string
		secret    []byte
		want      string
	}{
		{59, "SHA1", sha1, "94287082"},
		{1111111109, "SHA1", sha1, "07081804"},
		{1111111111, "SHA1", sha1, "14050471"},
		{1234567890, "SHA1", sha1, "89005924"},
		{2000000000, "SHA1", sha1, "69279037"},
		{20000000000, "SHA1", sha1, "65353130"},
		{59, "SHA256", sha256, "46119246"},
		{1111111109, "SHA256", sha256, "68084774"},
		{1111111111, "SHA256", sha256, "67062674"},
		{1234567890, "SHA256", sha256, "91819424"},
		{2000000000, "SHA256", sha256, "90698825"},
		{20000000000, "SHA256", sha256, "77737706"},
		{59, "SHA512", sha512, "90693936"},
		{1111111109, "SHA512", sha512, "25091201"},
		{1111111111, "SHA512", sha512, "99943326"},
		{1234567890, "SHA512", sha512, "93441116"},
		{2000000000, "SHA512", sha512, "38618901"},
		{20000000000, "SHA512", sha512, "47863826"},
	}
	for _, c := range cases {
		got, err := CodeRaw(c.secret, 8, 30, c.algorithm, time.Unix(c.at, 0))
		if err != nil || got != c.want {
			t.Errorf("T=%d %s = %q err=%v，期望 %s", c.at, c.algorithm, got, err, c.want)
		}
	}
}

func TestParseOTPAuthAndRawSecret(t *testing.T) {
	p, err := Parse("otpauth://totp/Serval:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Serval&digits=8&period=45&algorithm=SHA256")
	if err != nil {
		t.Fatal(err)
	}
	if p.Secret != "JBSWY3DPEHPK3PXP" || p.Digits != 8 || p.Period != 45 || p.Algorithm != "SHA256" {
		t.Fatalf("解析结果 %+v", p)
	}
	raw, err := Parse("jbsw y3dp ehpk 3pxp")
	if err != nil || raw.Secret != "JBSWY3DPEHPK3PXP" || raw.Digits != 0 {
		t.Fatalf("裸密钥 %+v err=%v", raw, err)
	}
	code, err := Code(Params{Secret: raw.Secret}, time.Unix(59, 0))
	if err != nil || len(code) != 6 {
		t.Fatalf("默认应是 6 位: %q %v", code, err)
	}

	if _, err := Parse("otpauth://hotp/demo?secret=JBSWY3DPEHPK3PXP"); err == nil || !strings.Contains(err.Error(), "不支持计数器") {
		t.Fatalf("HOTP 应被拒绝: %v", err)
	}
	if _, err := Parse("not a secret!!!"); err == nil {
		t.Fatal("非法密钥应失败")
	}
}
