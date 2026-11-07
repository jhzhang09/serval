package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
)

type transferKind int

const (
	transferDownload transferKind = iota
	transferUpload
)

// fileTransfer 记录单次文件上传或下载的状态。
// 进度字段在传输的 goroutine 里写、在界面上读，全部用原子操作；其余字段只在主线程读写。
type fileTransfer struct {
	id          int
	kind        transferKind
	remotePath  string
	localPath   string
	fileName    string
	total       atomic.Int64
	transferred atomic.Int64
	speed       atomic.Int64 // bytes / sec
	done        atomic.Bool
	err         string
	cancel      context.CancelFunc
}

// pendingUpload 是远端已有同名文件、等用户确认替换的上传。
type pendingUpload struct {
	tab                             *tab
	localPath, remotePath, fileName string
}

func (a *app) startDownload(t *tab, remotePath, fileName string) {
	if t.files == nil {
		return
	}
	localPath, err := a.chooseSavePath(fileName)
	if err != nil {
		a.toast(ui.Toast{Title: "无法打开保存对话框", Description: err.Error(), Type: "error"})
		return
	}
	if localPath == "" {
		return // 用户取消
	}
	a.runTransfer(t, transferDownload, remotePath, localPath, fileName)
}

// startUpload 选一个本地文件上传到当前目录。远端已有同名文件时先请用户确认替换。
// 检查是否存在要走网络，放在后台。
func (a *app) startUpload(t *tab) {
	if t.files == nil || t.dir == "" {
		return
	}
	localPath, err := a.chooseUploadFile()
	if err != nil {
		a.toast(ui.Toast{Title: "无法打开文件选择框", Description: err.Error(), Type: "error"})
		return
	}
	if localPath == "" {
		return
	}
	fileName := filepath.Base(localPath)
	remotePath := path.Join(t.dir, fileName)
	files := t.files
	go func() {
		exists, err := files.Exists(remotePath)
		a.post(func() {
			if !slices.Contains(a.tabs, t) {
				return
			}
			if err != nil {
				a.toast(ui.Toast{Title: "上传 " + fileName + " 失败", Description: transferReason(err), Type: "error"})
				return
			}
			if exists {
				a.pendingUpload = &pendingUpload{tab: t, localPath: localPath, remotePath: remotePath, fileName: fileName}
				return
			}
			a.runTransfer(t, transferUpload, remotePath, localPath, fileName)
		})
	}()
}

// uploadConfirmDialog 确认替换服务器上的同名文件。默认按钮是“取消”。
func (a *app) uploadConfirmDialog(c *ui.Context) {
	u := a.pendingUpload
	if u == nil {
		return
	}
	open := true
	choice := ui.AlertDialog(c, &open, "替换 "+u.fileName+"？",
		fmt.Sprintf("服务器上的 %s 里已经有同名文件，上传会替换它。", path.Dir(u.remotePath)), "替换", "取消")
	if choice == -1 && open {
		return
	}
	a.pendingUpload = nil
	if choice == 0 && slices.Contains(a.tabs, u.tab) {
		a.runTransfer(u.tab, transferUpload, u.remotePath, u.localPath, u.fileName)
	}
}

// runTransfer 在后台执行一次传输，进度通过原子字段给界面读取，结束后回到主线程提示结果。
func (a *app) runTransfer(t *tab, kind transferKind, remotePath, localPath, fileName string) {
	if t.files == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.nextID++
	tr := &fileTransfer{id: a.nextID, kind: kind, remotePath: remotePath, localPath: localPath, fileName: fileName, cancel: cancel}
	t.transfers = append(t.transfers, tr)
	files := t.files
	go func() {
		lastBytes, lastTime := int64(0), time.Now()
		progress := func(transferred, total int64) {
			tr.total.Store(total)
			tr.transferred.Store(transferred)
			if dur := time.Since(lastTime); dur >= 500*time.Millisecond {
				tr.speed.Store((transferred - lastBytes) * int64(time.Second) / int64(dur))
				lastBytes, lastTime = transferred, time.Now()
			}
		}
		var err error
		if kind == transferDownload {
			err = files.Download(ctx, remotePath, localPath, progress)
		} else {
			err = files.Upload(ctx, localPath, remotePath, progress)
		}
		a.post(func() {
			defer cancel()
			tr.done.Store(true)
			verb := "下载"
			if kind == transferUpload {
				verb = "上传"
			}
			switch {
			case ctx.Err() != nil:
				tr.err = "已取消"
				a.toast(ui.Toast{Title: "已取消" + verb + " " + fileName})
			case err != nil:
				tr.err = transferReason(err)
				a.toast(ui.Toast{Title: verb + " " + fileName + " 失败", Description: tr.err, Type: "error"})
			default:
				a.toast(ui.Toast{Title: "已" + verb + " " + fileName})
				if kind == transferUpload && slices.Contains(a.tabs, t) {
					a.openDir(t, t.dir)
				}
			}
		})
	}()
}

// transferReason 把文件操作的错误说成用户能看懂的原因。
func transferReason(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "没有权限。检查文件和目录的权限，或者换一个位置。"
	case errors.Is(err, os.ErrNotExist):
		return "文件或目录不存在，可能已经被删除或改名。"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "连接中断了，请重新连接后再试。"
	}
	return err.Error()
}

func (a *app) cancelTransfer(tr *fileTransfer) {
	if tr.cancel != nil {
		tr.cancel()
	}
}
