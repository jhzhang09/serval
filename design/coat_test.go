package design

import (
	"fmt"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

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

func TestCoatMutedIsCoolGray(t *testing.T) {
	for _, prod := range []bool{false, true} {
		muted := NewCoat("prod-web-01", prod).Muted()
		for _, c := range []ui.Color{muted.From, muted.To} {
			if c.A == 0 {
				t.Fatalf("置灰颜色不应透明: %+v", c)
			}
			spread := max(c.R, c.G, c.B) - min(c.R, c.G, c.B)
			if spread > 10 {
				t.Fatalf("prod=%v 置灰颜色饱和度过高（应呈冷灰）: %+v spread=%d", prod, c, spread)
			}
		}
	}
}

func TestNewCoatCacheDeterministicAndConcurrent(t *testing.T) {
	first := NewCoat("cache-seed-01", false)
	second := NewCoat("cache-seed-01", false)
	if first.From != second.From || first.To != second.To || len(first.Spots) != len(second.Spots) {
		t.Fatalf("缓存前后斑纹应完全一致: %+v vs %+v", first, second)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 80 {
				_ = NewCoat(fmt.Sprintf("concurrent-%d-%d", id, i%10), i%2 == 0)
			}
		}(g)
	}
	wg.Wait()
}
