package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"serval/core/sshconn"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// dragCopy 是为了拖到桌面而先下载到本地的一份副本。
//
// MyGo 的 FileData 只能交出已经存在的本地路径，不支持“松手时再生成文件”的文件承诺，
// 而且提供数据的函数必须立刻返回、不能在主线程上下载。所以第一次拖动只开始下载，
// 完成后再拖一次，才把临时文件交给访达或资源管理器。
type dragCopy struct {
	size      int64
	modUnix   int64
	local     string
	preparing bool
	// offered 表示已经有一次拖动把 local 交给了系统。测试用来确认拖动源接上了。
	offered bool
}

// bindDragOut 让文件行可以拖到桌面。只允许复制，避免文件管理器把临时文件移走。
func (a *app) bindDragOut(row ui.Element, t *tab, entry sshconn.Entry) {
	if t.local || t.files == nil {
		return
	}
	row.DragDataFrom(func() transfer.Data {
		return a.dragOutData(t, entry)
	}, transfer.DragOptions{Operations: transfer.Copy, Done: func(transfer.Result) {
		// 文件还没下好时这次拖动没有可提供的路径，系统会拒绝。错误是英文的，不展示。
	}})
}

func (a *app) dragOutData(t *tab, entry sshconn.Entry) transfer.Data {
	if local, ok := a.dragReady(entry); ok {
		data, err := transfer.FileData(local)
		if err == nil {
			if c := a.dragCopies[entry.Path]; c != nil {
				c.offered = true
			}
			return data
		}
	}
	a.prepareDragOut(t, entry)
	return transfer.Data{}
}

func (a *app) dragReady(entry sshconn.Entry) (string, bool) {
	c := a.dragCopies[entry.Path]
	if c == nil || c.preparing || c.local == "" || c.size != entry.Size || c.modUnix != entry.ModTime.UnixNano() {
		return "", false
	}
	if _, err := os.Stat(c.local); err != nil {
		return "", false
	}
	return c.local, true
}

// prepareDragOut 在后台把远程文件或文件夹下到临时目录。已经在下载或已经就绪时不重复开始。
func (a *app) prepareDragOut(t *tab, entry sshconn.Entry) {
	if t == nil || t.local || t.files == nil || !slices.Contains(a.tabs, t) {
		return
	}
	if _, ok := a.dragReady(entry); ok {
		return
	}
	if err := validateFileName(entry.Name); err != nil {
		a.toast(ui.Toast{Title: "无法拖出这个文件", Description: err.Error(), Type: "error"})
		return
	}
	if a.dragCopies == nil {
		a.dragCopies = map[string]*dragCopy{}
	}
	if c := a.dragCopies[entry.Path]; c != nil && c.preparing {
		a.toast(ui.Toast{Title: entry.Name + " 还在下载", Description: "完成后再拖一次，就可以拖到桌面。"})
		return
	}
	root, err := a.ensureDragRoot()
	if err != nil {
		a.toast(ui.Toast{Title: "无法准备拖出 " + entry.Name, Description: "没能创建临时文件夹。", Type: "error"})
		return
	}
	a.nextID++
	destDir := filepath.Join(root, strconv.Itoa(a.nextID))
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		a.toast(ui.Toast{Title: "无法准备拖出 " + entry.Name, Description: "没能创建临时文件夹。", Type: "error"})
		return
	}
	local := filepath.Join(destDir, entry.Name)
	copy := &dragCopy{size: entry.Size, modUnix: entry.ModTime.UnixNano(), preparing: true}
	a.dragCopies[entry.Path] = copy
	a.toast(ui.Toast{Title: "正在下载 " + entry.Name, Description: "下载完成后再拖一次，就能拖到桌面。"})

	files := t.files
	remote, name, isDir := entry.Path, entry.Name, entry.IsDir
	go func() {
		var skipped int
		var err error
		if isDir {
			skipped, err = files.DownloadTree(context.Background(), remote, local, nil)
		} else {
			err = files.Download(context.Background(), remote, local, nil)
		}
		a.post(func() {
			cur := a.dragCopies[remote]
			if cur != copy {
				os.RemoveAll(destDir)
				return
			}
			copy.preparing = false
			if !slices.Contains(a.tabs, t) || err != nil {
				os.RemoveAll(destDir)
				delete(a.dragCopies, remote)
				if err != nil && slices.Contains(a.tabs, t) {
					a.toast(ui.Toast{Title: "没法拖出 " + name, Description: transferReason(err), Type: "error"})
				}
				return
			}
			copy.local = local
			desc := "再拖一次，就可以拖到桌面。"
			if skipped > 0 {
				desc = fmt.Sprintf("跳过了 %d 个符号链接或特殊文件。%s", skipped, desc)
			}
			a.toast(ui.Toast{Title: name + " 可以拖出了", Description: desc})
		})
	}()
}

func (a *app) ensureDragRoot() (string, error) {
	if a.dragRoot != "" {
		return a.dragRoot, nil
	}
	dir, err := os.MkdirTemp("", "serval-drag-")
	if err != nil {
		return "", err
	}
	a.dragRoot = dir
	return dir, nil
}

// clearDragOut 删掉为拖出准备的临时文件。窗口关闭时调用。
func (a *app) clearDragOut() {
	if a.dragRoot != "" {
		os.RemoveAll(a.dragRoot)
	}
	a.dragRoot = ""
	a.dragCopies = nil
}
