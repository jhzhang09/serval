package main

import (
	"cmp"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"serval/design"

	"github.com/egoist/mygo/ui"
)

// localEntry 是本机目录里的一项。路径是绝对路径，只在主线程读写。
type localEntry struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
}

// paneDrag 是从文件抽屉某一栏拖出来的一项。local 为真表示来自本机栏。
type paneDrag struct {
	local bool
	path  string
	name  string
	dir   bool
}

// ensureLocalList 在抽屉第一次打开时列出本机目录。测试会事先放好 localDir，避免去读用户的主目录。
func (a *app) ensureLocalList() {
	if a.localGen > 0 {
		return
	}
	dir := a.localDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			a.localErr = "读不到本机的主目录。"
			a.localGen = 1
			return
		}
		dir = home
	}
	a.openLocal(dir)
}

// openLocal 在后台读取本机目录，主线程只应用最后一次请求的结果。
func (a *app) openLocal(dir string) {
	if dir == "" {
		return
	}
	a.localGen++
	gen := a.localGen
	a.localDir = dir
	go func() {
		entries, err := listLocal(dir)
		a.post(func() {
			if gen != a.localGen {
				return
			}
			if err != nil {
				a.localEntries = nil
				a.localErr = "读不到这个本地目录。"
				return
			}
			a.localEntries = entries
			a.localErr = ""
		})
	}()
}

func listLocal(dir string) ([]localEntry, error) {
	dents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]localEntry, 0, len(dents))
	for _, d := range dents {
		info, err := d.Info()
		if err != nil {
			continue
		}
		// 不跟随符号链接，避免点进去绕回上级目录。
		entries = append(entries, localEntry{
			Name:  d.Name(),
			Path:  filepath.Join(dir, d.Name()),
			IsDir: d.IsDir(),
			Size:  info.Size(),
		})
	}
	slices.SortFunc(entries, func(a, b localEntry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return entries, nil
}

// acceptPaneDrop 把拖到另一栏的文件传过去。同一栏里松开什么都不做。
// 下到本机时不覆盖已有文件；传到远端时沿用上传的替换确认。
func (a *app) acceptPaneDrop(t *tab, destLocal bool, destDir string, item paneDrag) {
	if t == nil || item.local == destLocal || destDir == "" || item.name == "" || item.path == "" {
		return
	}
	if item.local {
		if t.local || t.files == nil {
			return
		}
		if item.dir {
			a.planFolderUpload(t, item.path, destDir)
			return
		}
		a.uploadTo(t, item.path, path.Join(destDir, item.name))
		return
	}
	if t.local || t.files == nil {
		return
	}
	if err := validateFileName(item.name); err != nil {
		a.toast(ui.Toast{Title: "无法下载这个文件", Description: err.Error(), Type: "error"})
		return
	}
	local := filepath.Join(destDir, item.name)
	if _, err := os.Lstat(local); err == nil {
		a.toast(ui.Toast{Title: "没有下载 " + item.name, Description: "本地已经有同名的文件或文件夹，请先把原来的移走。", Type: "error"})
		return
	}
	if item.dir {
		a.runFolderDownload(t, item.path, local, item.name)
		return
	}
	// 拖放时先查一次是为了即时反馈；下载完成、改名落盘前 DownloadIfAbsent
	// 会再查一次，兜住“检查之后、落盘之前”本地出现同名文件的窗口。
	a.runTransfer(t, transferDownload, item.path, local, item.name, true)
}

// drawerPath 返回文件抽屉标题栏显示的路径；路径过长时保留末尾层级，避免右端截断遮住当前目录名。
func drawerPath(p string) string {
	if len(p) <= 38 {
		return p
	}
	return shortPath(p)
}

func (a *app) localColumn(c *ui.Context, colors design.Colors, t *tab) {
	theme := c.Theme()
	col := ui.Column(c).Key("localPane").Grow(1).MinWidth(0).MinHeight(0)
	if item, ok := ui.Drop[paneDrag](col); ok {
		a.acceptPaneDrop(t, true, a.localDir, item)
	}
	if over, ok := ui.DragOver[paneDrag](col); ok && !over.local {
		col.Background(theme.Accent.Alpha(0.08))
	}
	col.Children(func() {
		ui.Row(c).Height(32).Shrink(0).Padding(4, 6, 0, 14).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "本地").FontSize(design.TextSmall).TextColor(theme.TextMuted).Grow(1).MinWidth(0).SingleLine()
			parent := filepath.Dir(a.localDir)
			iconButton(c, colors, "arrow-up", "本地上一级", func() { a.openLocal(parent) }).
				Disabled(a.localDir == "" || parent == a.localDir)
			iconButton(c, colors, "refresh-cw", "刷新本地", func() { a.openLocal(a.localDir) }).
				Disabled(a.localDir == "")
		})
		ui.Row(c).Height(26).Shrink(0).Padding(0, 10, 4, 14).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Children(func() {
			title := drawerPath(filepath.ToSlash(a.localDir))
			if a.localDir == "" || a.localDir == "." {
				title = "本机"
			}
			ui.Text(c, title).Key("localTitle").FontSize(design.TextSmall).FontWeight(600).
				Grow(1).MinWidth(0).SingleLine().Tooltip(a.localDir)
		})
		if a.localErr != "" {
			ui.Text(c, a.localErr).TextColor(colors.Danger).FontSize(design.TextSmall).Padding(8, 14).Selectable()
		}
		entries := a.localEntries
		ui.List(c, nil, len(entries), func(i int) {
			a.localFileRow(c, colors, t, entries[i])
		}).Grow(1).MinHeight(0).Padding(6, 6, 8, 6).Children(func() {
			if len(entries) == 0 && a.localErr == "" && a.localGen > 0 {
				ui.Text(c, "本地目录是空的").TextColor(theme.TextMuted).Padding(8, 8)
			}
		})
	})
}

func (a *app) localFileRow(c *ui.Context, colors design.Colors, t *tab, entry localEntry) {
	theme := c.Theme()
	content := func() {
		icon := "file"
		color := colors.TextFaint
		if entry.IsDir {
			icon, color = "folder", theme.Accent
		}
		ui.Icon(c, design.Icon(icon)).FontSize(14).TextColor(color)
		ui.Text(c, entry.Name).Grow(1).MinWidth(0).SingleLine()
		if !entry.IsDir {
			ui.Text(c, formatSize(entry.Size)).FontSize(design.TextSmall).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
		}
	}
	// 标签带上“本地”，和远端同名文件同时出现时，点击和读屏不会撞在一起。
	row := ui.ButtonBase(c).Key("local:"+entry.Path).Label("本地 "+entry.Name).Height(28).Padding(0, 8).Gap(8).
		Justify(ui.Start).AlignItems(ui.Center).Radius(design.RadiusRow - 2)
	row.Drag(paneDrag{local: true, path: entry.Path, name: entry.Name, dir: entry.IsDir})
	if entry.IsDir {
		if item, ok := ui.Drop[paneDrag](row); ok {
			a.acceptPaneDrop(t, true, entry.Path, item)
		}
		row.OnClick(func() { a.openLocal(entry.Path) })
	}
	if row.Hovered() {
		row.Background(colors.Hover)
	}
	row.Children(content)
}

func (a *app) remoteColumn(c *ui.Context, colors design.Colors, t *tab) {
	theme := c.Theme()
	col := ui.Column(c).Key("remotePane").Grow(1).MinWidth(0).MinHeight(0)
	if item, ok := ui.Drop[paneDrag](col); ok {
		a.acceptPaneDrop(t, false, t.dir, item)
	}
	if over, ok := ui.DragOver[paneDrag](col); ok && over.local {
		col.Background(theme.Accent.Alpha(0.08))
	}
	col.Children(func() {
		ui.Row(c).Height(32).Shrink(0).Padding(4, 6, 0, 14).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "远程").FontSize(design.TextSmall).TextColor(theme.TextMuted).Grow(1).MinWidth(0).SingleLine()
			iconButton(c, colors, "plus", "上传文件", func() { a.startUpload(t) }).Disabled(t.dir == "" || t.local)
			iconButton(c, colors, "folder-up", "上传文件夹", func() { a.startUploadFolder(t) }).Disabled(t.dir == "" || t.local)
			iconButton(c, colors, "folder-plus", "新建文件夹", func() { a.openNewFolder(t) }).Disabled(t.dir == "" || t.local)
			iconButton(c, colors, "arrow-up", "上一级", func() { a.openDir(t, path.Dir(t.dir)) }).Disabled(t.dir == "" || t.dir == "/")
			iconButton(c, colors, "refresh-cw", "刷新", func() { a.openDir(t, t.dir) }).Disabled(t.dir == "")
		})
		ui.Row(c).Height(26).Shrink(0).Padding(0, 10, 4, 14).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(colors.Hairline).Children(func() {
			title := drawerPath(t.dir)
			if t.dir == "" {
				title = "文件"
			}
			ui.Text(c, title).Key("filesTitle").FontSize(design.TextSmall).FontWeight(600).
				Grow(1).MinWidth(0).SingleLine().Tooltip(t.dir)
		})
		if t.filesError != "" {
			ui.Text(c, t.filesError).TextColor(colors.Danger).FontSize(design.TextSmall).Padding(8, 14).Selectable()
		}
		if t.filesBusy {
			ui.Text(c, "正在打开文件…").TextColor(theme.TextMuted).Padding(10, 14)
		}
		if t.local {
			ui.Text(c, "文件抽屉用于浏览远程主机的文件。本机的文件请在访达或资源管理器里查看。").
				TextColor(theme.TextMuted).FontSize(design.TextSmall).Padding(10, 14)
		}
		entries := t.entries
		ui.List(c, nil, len(entries), func(i int) {
			a.fileRow(c, colors, t, entries[i])
		}).Grow(1).MinHeight(0).Padding(6, 6, 8, 6).Children(func() {
			if len(entries) == 0 && t.files != nil && t.filesError == "" && !t.local {
				ui.Text(c, "这个目录是空的").TextColor(theme.TextMuted).Padding(8, 8)
			}
		})
	})
}
