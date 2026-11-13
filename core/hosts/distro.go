package hosts

import (
	"bufio"
	"strings"
)

// 发行版与操作系统标识常量。
const (
	DistroAuto     = ""
	DistroUbuntu   = "ubuntu"
	DistroDebian   = "debian"
	DistroCentOS   = "centos"
	DistroRedHat   = "redhat"
	DistroFedora   = "fedora"
	DistroArch     = "arch"
	DistroAlpine   = "alpine"
	DistroRocky    = "rocky"
	DistroAlma     = "alma"
	DistroOpenSUSE = "opensuse"
	DistroMacOS    = "macos"
	DistroFreeBSD  = "freebsd"
	DistroOpenBSD  = "openbsd"
	DistroWindows  = "windows"
	DistroLinux    = "linux"
)

// DistroOption 是供界面下拉框选择的主机系统项。
type DistroOption struct {
	Key   string
	Label string
}

// SupportedDistros 返回界面支持手动选择的发行版列表。
func SupportedDistros() []DistroOption {
	return []DistroOption{
		{Key: DistroAuto, Label: "自动识别"},
		{Key: DistroUbuntu, Label: "Ubuntu"},
		{Key: DistroDebian, Label: "Debian"},
		{Key: DistroCentOS, Label: "CentOS"},
		{Key: DistroRedHat, Label: "Red Hat (RHEL)"},
		{Key: DistroRocky, Label: "Rocky Linux"},
		{Key: DistroAlma, Label: "AlmaLinux"},
		{Key: DistroFedora, Label: "Fedora"},
		{Key: DistroArch, Label: "Arch Linux"},
		{Key: DistroAlpine, Label: "Alpine Linux"},
		{Key: DistroOpenSUSE, Label: "openSUSE"},
		{Key: DistroMacOS, Label: "macOS"},
		{Key: DistroFreeBSD, Label: "FreeBSD"},
		{Key: DistroOpenBSD, Label: "OpenBSD"},
		{Key: DistroWindows, Label: "Windows"},
		{Key: DistroLinux, Label: "通用 Linux"},
	}
}

// NormalizeDistro 将输入字符串标准化为受支持的 Distro 标识；无法识别时返回空串。
func NormalizeDistro(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "ubuntu":
		return DistroUbuntu
	case "debian":
		return DistroDebian
	case "centos":
		return DistroCentOS
	case "rhel", "redhat", "red hat":
		return DistroRedHat
	case "rocky", "rocky linux":
		return DistroRocky
	case "alma", "almalinux", "alma linux":
		return DistroAlma
	case "fedora":
		return DistroFedora
	case "arch", "archlinux", "arch linux", "manjaro":
		return DistroArch
	case "alpine", "alpinelinux":
		return DistroAlpine
	case "opensuse", "suse", "opensuse-leap", "opensuse-tumbleweed":
		return DistroOpenSUSE
	case "macos", "darwin", "mac", "os x", "apple":
		return DistroMacOS
	case "freebsd":
		return DistroFreeBSD
	case "openbsd":
		return DistroOpenBSD
	case "windows", "win", "cygwin", "mingw", "msys":
		return DistroWindows
	case "linux", "gnu/linux":
		return DistroLinux
	default:
		return ""
	}
}

// DetectDistro 解析远端命令探针结果（通常来自 /etc/os-release 或 uname -s）。
func DetectDistro(osReleaseContent, unameOutput string) string {
	if osReleaseContent != "" {
		fields := parseOSRelease(osReleaseContent)
		id := strings.ToLower(fields["id"])
		if norm := NormalizeDistro(id); norm != "" {
			return norm
		}
		// 检查 ID_LIKE（如 rocky/alma 会写 ID_LIKE="rhel centos fedora"）
		for _, part := range strings.Fields(strings.ToLower(fields["id_like"])) {
			if norm := NormalizeDistro(part); norm != "" {
				return norm
			}
		}
		name := strings.ToLower(fields["name"] + " " + fields["pretty_name"])
		switch {
		case strings.Contains(name, "ubuntu"):
			return DistroUbuntu
		case strings.Contains(name, "debian"):
			return DistroDebian
		case strings.Contains(name, "centos"):
			return DistroCentOS
		case strings.Contains(name, "red hat") || strings.Contains(name, "rhel"):
			return DistroRedHat
		case strings.Contains(name, "rocky"):
			return DistroRocky
		case strings.Contains(name, "alma"):
			return DistroAlma
		case strings.Contains(name, "fedora"):
			return DistroFedora
		case strings.Contains(name, "arch"):
			return DistroArch
		case strings.Contains(name, "alpine"):
			return DistroAlpine
		case strings.Contains(name, "suse"):
			return DistroOpenSUSE
		}
	}

	u := strings.TrimSpace(unameOutput)
	switch {
	case strings.EqualFold(u, "Darwin"):
		return DistroMacOS
	case strings.EqualFold(u, "FreeBSD"):
		return DistroFreeBSD
	case strings.EqualFold(u, "OpenBSD"):
		return DistroOpenBSD
	case strings.HasPrefix(strings.ToLower(u), "cygwin") ||
		strings.HasPrefix(strings.ToLower(u), "mingw") ||
		strings.HasPrefix(strings.ToLower(u), "msys"):
		return DistroWindows
	case strings.EqualFold(u, "Linux"):
		return DistroLinux
	}

	return ""
}

func parseOSRelease(content string) map[string]string {
	res := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		res[strings.ToLower(k)] = v
	}
	return res
}
