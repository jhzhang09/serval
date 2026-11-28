package ai

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	raw := strings.Join([]string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nabcdef123456\n-----END OPENSSH PRIVATE KEY-----",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig",
		"AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE",
		"OPENAI_API_KEY=sk-proj-1234567890abcdef1234567890",
		"DB_PASSWORD=\"superSecretPass99\"",
		"https://admin:mySecretPass@db.internal:5432/prod",
		"PasswordAuthentication yes",
		"DEBUG=false",
	}, "\n")

	redacted, count := RedactSecrets(raw)
	if count < 6 {
		t.Fatalf("expected at least 6 redactions, got %d\n%s", count, redacted)
	}
	for _, leak := range []string{
		"abcdef123456",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		"AKIAIOSFODNN7EXAMPLE",
		"sk-proj-1234567890abcdef1234567890",
		"superSecretPass99",
		"mySecretPass",
	} {
		if strings.Contains(redacted, leak) {
			t.Fatalf("secret leaked after redaction: %q in:\n%s", leak, redacted)
		}
	}
	if !strings.Contains(redacted, "PasswordAuthentication yes") {
		t.Fatalf("non-secret SSH config line should be preserved:\n%s", redacted)
	}
	if !strings.Contains(redacted, "DEBUG=false") {
		t.Fatalf("boolean flag should be preserved:\n%s", redacted)
	}
}
