package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"serval/core/sshconn"
	"serval/design"

	"github.com/egoist/mygo/ui"
)

// maxEditorBytes 是内置编辑器愿意读进内存的上限。
// 文本框要留着全文才能编辑，再大的文件就请用户下载后用别的程序打开。
const maxEditorBytes = 1 << 20

// editorState 是正在编辑的一个远程文本文件。字段只在主线程读写。
type editorState struct {
	open, fresh bool
	loading     bool
	tab         *tab
	path, name  string
	buf         *ui.TextBuffer
	// size 和 modUnix 是打开时（或上次成功保存时）远端的大小和修改时间，精确到秒。
	size    int64
	modUnix int64
	// version 是上次和远端一致时的文本版本。和 buf.Version() 不同就表示还没保存。
	version uint64
	dirty   bool
	busy    bool
	err     string

	askDiscard   bool
	askOverwrite bool
	// closeAfterDiscard 是“放弃修改”确认后要一并关闭的标签页。
	// 从标签页触发确认时记录；取消或从编辑器自己的关闭按钮触发时保持为空。
	closeAfterDiscard *tab
}

func (a *app) openEditor(t *tab, remotePath, name string) {
	if t == nil || t.local || t.files == nil || a.editor.loading {
		return
	}
	if a.editor.open && a.editor.dirty {
		a.toast(ui.Toast{Title: "请先处理正在编辑的 " + a.editor.name})
		return
	}
	if err := validateFileName(name); err != nil {
		a.toast(ui.Toast{Title: "不能编辑这个文件", Description: err.Error(), Type: "error"})
		return
	}
	a.editor.loading = true
	files := t.files
	go func() {
		data, mod, err := files.ReadLimited(context.Background(), remotePath, maxEditorBytes)
		a.post(func() {
			a.editor.loading = false
			if !slices.Contains(a.tabs, t) {
				return
			}
			if err != nil {
				a.toast(ui.Toast{Title: "不能编辑 " + name, Description: editorReason(err), Type: "error"})
				return
			}
			buf := ui.NewTextBuffer(string(data))
			a.editor = editorState{
				open: true, fresh: true, tab: t, path: remotePath, name: name,
				buf: buf, size: int64(len(data)), modUnix: mod.Unix(), version: buf.Version(),
			}
		})
	}()
}

func editorReason(err error) string {
	switch {
	case errors.Is(err, sshconn.ErrFileTooLarge):
		return fmt.Sprintf("文件超过 %d MB，编辑器打不开。请下载后再用别的程序编辑。", maxEditorBytes>>20)
	case errors.Is(err, sshconn.ErrNotText):
		return "这不是 UTF-8 文本（或含有二进制数据），编辑器打不开。"
	default:
		return transferReason(err)
	}
}

func (a *app) requestCloseEditor() {
	if a.editor.dirty {
		a.editor.askDiscard = true
		a.editor.closeAfterDiscard = nil
		return
	}
	a.editor = editorState{}
}

func (a *app) saveEditor(force bool) {
	ed := &a.editor
	if !ed.open || ed.busy || ed.buf == nil || ed.tab == nil || ed.tab.files == nil {
		return
	}
	if !slices.Contains(a.tabs, ed.tab) {
		a.editor = editorState{}
		return
	}
	text := ed.buf.String()
	if strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
		ed.err = "内容不是文本，没有保存。"
		return
	}
	data := []byte(text)
	savedVersion := ed.buf.Version()
	ed.busy = true
	ed.err = ""
	files, path, name := ed.tab.files, ed.path, ed.name
	expectSize, expectUnix := ed.size, ed.modUnix
	t := ed.tab
	go func() {
		err := files.WriteFile(context.Background(), path, data, expectSize, expectUnix, force)
		stamp := editorMod{}
		if err == nil {
			stamp = edMod(files, path)
			err = stamp.err
		}
		a.post(func() {
			if a.editor.path != path || a.editor.buf == nil {
				return
			}
			a.editor.busy = false
			if errors.Is(err, sshconn.ErrRemoteChanged) {
				a.editor.askOverwrite = true
				return
			}
			if err != nil {
				a.editor.err = editorReason(err)
				a.toast(ui.Toast{Title: "没有保存 " + name, Description: a.editor.err, Type: "error"})
				return
			}
			a.editor.size = stamp.size
			a.editor.modUnix = stamp.unix
			if a.editor.buf.Version() == savedVersion {
				a.editor.version = savedVersion
				a.editor.dirty = false
			}
			a.toast(ui.Toast{Title: "已保存 " + name})
			if slices.Contains(a.tabs, t) {
				a.openDir(t, t.dir)
			}
		})
	}()
}

type editorMod struct {
	size int64
	unix int64
	err  error
}

func edMod(files *sshconn.Files, path string) editorMod {
	size, mod, _, err := files.Stat(path)
	if err != nil {
		return editorMod{err: err}
	}
	return editorMod{size: size, unix: mod.Unix()}
}

func (a *app) editorDialog(c *ui.Context, colors design.Colors) {
	ed := &a.editor
	if ed.open && ed.tab != nil && !slices.Contains(a.tabs, ed.tab) {
		a.editor = editorState{}
		return
	}
	if ed.askDiscard || ed.askOverwrite {
		a.editorConfirm(c)
		return
	}
	if !ed.open || a.setup.open || ed.buf == nil {
		return
	}
	if ed.buf.Version() != ed.version {
		ed.dirty = true
	}
	t := c.Theme()
	windowW, windowH := c.Size()
	panelW := float32(680)
	if windowW > 0 {
		panelW = min(max(windowW-120, 520), 840)
		if windowW < 560 {
			panelW = max(windowW-32, 320)
		}
	}
	editorH := float32(360)
	if windowH > 0 {
		editorH = min(max(windowH-240, 260), 620)
		if windowH < 460 {
			editorH = max(windowH-180, 160)
		}
	}
	title := "编辑 " + ed.name
	saveText := "保存"
	if ed.busy {
		saveText = "正在保存…"
	}
	panel := sheet(c, colors, &ed.open, &ed.fresh, panelW, 22, func() {
		ui.Column(c).Gap(10).OnShortcut(chromeShortcut(), ui.KeyS, func() {
			if !ed.busy {
				a.saveEditor(false)
			}
		}).Children(func() {
			ui.Column(c).Gap(3).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, title).FontSize(design.TextHeading).FontWeight(600).SingleLine().MinWidth(0).Grow(1)
					if ed.dirty {
						chip(c, "未保存", t.Accent, t.Accent.Alpha(0.14))
					}
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, ed.path).FontSize(design.TextSmall).TextColor(t.TextMuted).
						SingleLine().Ellipsis("…").MinWidth(0).Grow(1).Tooltip(ed.path)
					ui.Text(c, shortcutText("S")+" 保存").FontSize(design.TextSmall).TextColor(colors.TextFaint).Shrink(0)
				})
			})
			if ed.err != "" {
				ui.Text(c, ed.err).FontSize(design.TextSmall).TextColor(colors.Danger).Selectable()
			}
			c.Key("editorText")
			ui.TextAreaBuffer(c, ed.buf).Label("编辑内容").Height(editorH).
				Font(design.FontTerminal).FontSize(design.TextBody).
				ReadOnly(ed.busy).AutoFocus()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				ui.Button(c, "关闭").Key("editorClose").Disabled(ed.busy).OnClick(func() { a.requestCloseEditor() })
				ui.PrimaryButton(c, saveText).Key("editorSave").Disabled(ed.busy).OnClick(func() { a.saveEditor(false) })
			})
		})
	})
	if panel.Valid() && panel.Dismissed() {
		if ed.dirty {
			ed.open = true
			ed.askDiscard = true
		} else {
			a.editor = editorState{}
		}
	}
}

func (a *app) editorConfirm(c *ui.Context) {
	ed := &a.editor
	if ed.askDiscard {
		open := true
		choice := ui.AlertDialog(c, &open, "放弃修改？", ed.name+" 有未保存的修改。", "放弃修改", "取消")
		if choice == -1 && open {
			return
		}
		ed.askDiscard = false
		if choice == 0 {
			// 确认来自关标签时，放弃修改后把那个标签也关掉。
			t := ed.closeAfterDiscard
			a.editor = editorState{}
			if t != nil && slices.Contains(a.tabs, t) {
				a.closeTab(t)
			}
		} else {
			ed.closeAfterDiscard = nil
		}
		return
	}
	if !ed.askOverwrite {
		return
	}
	open := true
	choice := ui.AlertDialog(c, &open, "服务器上的文件已变化", "保存会覆盖服务器上的新内容。", "仍然保存", "取消")
	if choice == -1 && open {
		return
	}
	ed.askOverwrite = false
	if choice == 0 && slices.Contains(a.tabs, ed.tab) {
		a.saveEditor(true)
	}
}
