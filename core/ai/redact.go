package ai

import (
	"regexp"
	"strings"
)

var (
	rePEMPrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	reAWSAccessKey  = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	reAnthropicKey  = regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`)
	reOpenAIKey     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)
	reGitHubToken   = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)
	reGitLabToken   = regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)
	reSlackToken    = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)
	reGoogleAPIKey  = regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)
	reAuthHeader    = regexp.MustCompile(`(?i)\b(Authorization\s*:\s*(?:Bearer|Basic)\s+)([^\s"';]+)`)
	reURLCreds      = regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)([^/\s@]{3,})(@)`)
	reSensitiveKV   = regexp.MustCompile(`(?im)\b([A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|API_KEY|APIKEY|ACCESS_KEY|SECRET_KEY|ACCESS_TOKEN|AUTH_TOKEN|PRIVATE_KEY|JWT_SECRET|DB_PASS|REDIS_PASS)[A-Z0-9_]*)\s*([=:])\s*("([^"\r\n]{6,})"|'([^'\r\n]{6,})'|([^\s"'#;,\r\n]{6,}))`)
)

func isIgnoredSecretValue(v string) bool {
	low := strings.ToLower(strings.TrimSpace(v))
	if low == "" || strings.HasPrefix(low, "$") || strings.Contains(low, "已脱敏") || strings.Contains(low, "***") {
		return true
	}
	switch low {
	case "true", "false", "yes", "no", "none", "null", "undefined", "empty", "changeme", "<redacted>":
		return true
	}
	return false
}

// RedactSecrets 对发送给模型的文本执行常见私钥、API Token、URL 凭据与敏感键值对脱敏。
// 返回脱敏后的文本以及替换次数。
func RedactSecrets(s string) (string, int) {
	if s == "" {
		return "", 0
	}
	count := 0
	replaceAll := func(re *regexp.Regexp, src, repl string) string {
		return re.ReplaceAllStringFunc(src, func(_ string) string {
			count++
			return repl
		})
	}

	out := replaceAll(rePEMPrivateKey, s, "[已脱敏:PRIVATE_KEY]")
	out = replaceAll(reAWSAccessKey, out, "[已脱敏:AWS_KEY]")
	out = replaceAll(reAnthropicKey, out, "[已脱敏:API_KEY]")
	out = replaceAll(reOpenAIKey, out, "[已脱敏:API_KEY]")
	out = replaceAll(reGitHubToken, out, "[已脱敏:GH_TOKEN]")
	out = replaceAll(reGitLabToken, out, "[已脱敏:GL_TOKEN]")
	out = replaceAll(reSlackToken, out, "[已脱敏:SLACK_TOKEN]")
	out = replaceAll(reGoogleAPIKey, out, "[已脱敏:GOOGLE_KEY]")

	out = reAuthHeader.ReplaceAllStringFunc(out, func(m string) string {
		sub := reAuthHeader.FindStringSubmatch(m)
		if len(sub) != 3 || isIgnoredSecretValue(sub[2]) {
			return m
		}
		count++
		return sub[1] + "[已脱敏]"
	})

	out = reURLCreds.ReplaceAllStringFunc(out, func(m string) string {
		sub := reURLCreds.FindStringSubmatch(m)
		if len(sub) != 4 || isIgnoredSecretValue(sub[2]) {
			return m
		}
		count++
		return sub[1] + "[已脱敏]" + sub[3]
	})

	out = reSensitiveKV.ReplaceAllStringFunc(out, func(m string) string {
		sub := reSensitiveKV.FindStringSubmatch(m)
		if len(sub) < 7 {
			return m
		}
		key, sep := sub[1], sub[2]
		val := sub[4]
		quote := `"`
		if val == "" {
			val = sub[5]
			quote = `'`
		}
		if val == "" {
			val = sub[6]
			quote = ""
		}
		if isIgnoredSecretValue(val) {
			return m
		}
		count++
		return key + sep + quote + "[已脱敏]" + quote
	})

	return out, count
}
