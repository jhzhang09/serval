package hosts

import "testing"

func TestDetectDistro(t *testing.T) {
	ubuntuOS := `NAME="Ubuntu"
VERSION="24.04 LTS (Noble Numbat)"
ID=ubuntu
ID_LIKE=debian
PRETTY_NAME="Ubuntu 24.04 LTS"
`
	if got := DetectDistro(ubuntuOS, "Linux"); got != DistroUbuntu {
		t.Fatalf("want ubuntu, got %q", got)
	}

	debianOS := `ID=debian
PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
`
	if got := DetectDistro(debianOS, "Linux"); got != DistroDebian {
		t.Fatalf("want debian, got %q", got)
	}

	alpineOS := `NAME="Alpine Linux"
ID=alpine
VERSION_ID=3.20.0
PRETTY_NAME="Alpine Linux v3.20"
`
	if got := DetectDistro(alpineOS, "Linux"); got != DistroAlpine {
		t.Fatalf("want alpine, got %q", got)
	}

	rockyOS := `NAME="Rocky Linux"
ID="rocky"
ID_LIKE="rhel centos fedora"
VERSION_ID="9.4"
`
	if got := DetectDistro(rockyOS, "Linux"); got != DistroRocky {
		t.Fatalf("want rocky, got %q", got)
	}

	// 仅有 uname
	if got := DetectDistro("", "Darwin\n"); got != DistroMacOS {
		t.Fatalf("want macos, got %q", got)
	}
	if got := DetectDistro("", "FreeBSD"); got != DistroFreeBSD {
		t.Fatalf("want freebsd, got %q", got)
	}
	if got := DetectDistro("", "CYGWIN_NT-10.0"); got != DistroWindows {
		t.Fatalf("want windows, got %q", got)
	}
	if got := DetectDistro("", "Linux"); got != DistroLinux {
		t.Fatalf("want linux, got %q", got)
	}
}

func TestNormalizeDistro(t *testing.T) {
	cases := map[string]string{
		"Ubuntu":      DistroUbuntu,
		"Arch Linux":  DistroArch,
		"darwin":      DistroMacOS,
		"RHEL":        DistroRedHat,
		"unknown-foo": "",
	}
	for in, want := range cases {
		if got := NormalizeDistro(in); got != want {
			t.Errorf("NormalizeDistro(%q) = %q, want %q", in, got, want)
		}
	}
}
