package design

import "testing"

func TestDistroIconNameAndEmbed(t *testing.T) {
	distros := []string{
		"ubuntu",
		"debian",
		"centos",
		"redhat",
		"rhel",
		"fedora",
		"arch",
		"alpine",
		"rocky",
		"alma",
		"opensuse",
		"suse",
		"macos",
		"darwin",
		"windows",
		"freebsd",
		"openbsd",
		"linux",
	}

	for _, d := range distros {
		name := DistroIconName(d)
		if name == "" {
			t.Errorf("Distro %q should have an icon name", d)
			continue
		}
		// 验证每个发行版对应的 SVG 文件均在 embed.FS 且能被解析
		svg := Icon(name)
		if svg == nil {
			t.Errorf("Distro %q with icon %q failed to load SVG", d, name)
		}
	}

	if DistroIconName("") != "" {
		t.Error("Empty distro should return empty icon name")
	}
	if DistroIconName("unknown-xyz") != "" {
		t.Error("Unknown distro should return empty icon name")
	}
}
