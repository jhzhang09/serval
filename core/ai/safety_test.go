package ai

import "testing"

func TestSafetyParserBypassPrevention(t *testing.T) {
	shouldBlock := []string{
		"echo ok & rm -rf /tmp/test",
		"sudo -u root rm -rf /tmp/test",
		"sudo -E -H rm -rf /var/log",
		`\rm -rf /tmp/test`,
		`r\m -rf /tmp/test`,
		"echo x | sudo -u root sh",
		"echo root | tee /etc/passwd",
		"rm /etc/nginx/nginx.conf",
		"rm /root/.ssh/authorized_keys",
	}
	for _, cmd := range shouldBlock {
		if !IsDangerousCommand(cmd) {
			t.Errorf("expected dangerous command to be blocked: %q", cmd)
		}
	}

	shouldNotReadOnly := []string{
		"awk 'BEGIN{system(\"id\")}'",
		"top",
		"htop",
		"less /var/log/syslog",
		"more /var/log/syslog",
		"sed 's/a/b/e' /etc/hosts",
		"sed -f /tmp/evil.sed /etc/hosts",
		"echo ok & touch /tmp/x",
	}
	for _, cmd := range shouldNotReadOnly {
		if IsReadOnlyProbeCommand(cmd) {
			t.Errorf("expected non-read-only command to be rejected: %q", cmd)
		}
	}

	shouldReadOnly := []string{
		"top -b -n 1",
		"sed -n '1,20p' /var/log/syslog",
		"cat /var/log/nginx/error.log 2>&1 | tail -n 30",
	}
	for _, cmd := range shouldReadOnly {
		if !IsReadOnlyProbeCommand(cmd) {
			t.Errorf("expected read-only command to be allowed: %q", cmd)
		}
	}
}

func TestBaseCommandProgramAndSensitivePath(t *testing.T) {
	if got := BaseCommandProgram("sudo -u www-data systemctl restart nginx"); got != "systemctl" {
		t.Fatalf("expected systemctl, got %q", got)
	}
	if got := BaseCommandProgram("echo ok && systemctl restart nginx"); got != "" {
		t.Fatalf("expected empty for compound command, got %q", got)
	}
	if got := BaseCommandProgram("echo ok > /tmp/a"); got != "" {
		t.Fatalf("expected empty for redirect command, got %q", got)
	}
	if !IsSensitiveWritePath("/etc/nginx/nginx.conf") || !IsSensitiveWritePath("/root/.ssh/authorized_keys") {
		t.Fatal("expected sensitive write paths to be flagged")
	}
	if IsSensitiveWritePath("/var/www/app/config.yaml") {
		t.Fatal("unexpected sensitive flag for app config")
	}
}
