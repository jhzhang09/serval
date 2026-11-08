package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	a.uploadTo(t, localPath, path.Join(t.dir, filepath.Base(localPath)))
}

// uploadTo 把本地文件上传到 remotePath。远端已有同名文件时先请用户确认替换；
// 检查是否存在要走网络，放在后台。
func (a *app) uploadTo(t *tab, localPath, remotePath string) {
	fileName := filepath.Base(localPath)
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
				a.pendingUploads = append(a.pendingUploads, &pendingUpload{tab: t, localPath: localPath, remotePath: remotePath, fileName: fileName})
				return
			}
			a.runTransfer(t, transferUpload, remotePath, localPath, fileName)
		})
	}()
}

// uploadConfirmDialog 确认替换服务器上的同名文件。默认按钮是“取消”。
func (a *app) uploadConfirmDialog(c *ui.Context) {
	if len(a.pendingUploads) == 0 {
		return
	}
	u := a.pendingUploads[0]
	open := true
	choice := ui.AlertDialog(c, &open, "替换 "+u.fileName+"？",
		fmt.Sprintf("服务器上的 %s 里已经有同名文件，上传会替换它。", path.Dir(u.remotePath)), "替换", "取消")
	if choice == -1 && open {
		return
	}
	a.pendingUploads = a.pendingUploads[1:]
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

type fileOpKind int

const (
	fileOpNone fileOpKind = iota
	fileOpNewFolder
	fileOpRename
	fileOpPermissions
)

type fileOpState struct {
	open  bool
	fresh bool
	kind  fileOpKind
	tab   *tab
	path  string
	name  string
	input string
	err   string
	// busy 表示操作正在后台执行，期间按钮禁用。
	busy bool
}

type fileDeleteState struct {
	tab   *tab
	path  string
	name  string
	isDir bool
}

func (a *app) openNewFolder(t *tab) {
	if t.files == nil || t.dir == "" {
		return
	}
	a.fileOp = fileOpState{
		open:  true,
		fresh: true,
		kind:  fileOpNewFolder,
		tab:   t,
		path:  t.dir,
		input: "新建文件夹",
	}
}

func (a *app) openRenameFile(t *tab, fullPath, name string) {
	if t.files == nil {
		return
	}
	a.fileOp = fileOpState{
		open:  true,
		fresh: true,
		kind:  fileOpRename,
		tab:   t,
		path:  fullPath,
		name:  name,
		input: name,
	}
}

func (a *app) openPermissions(t *tab, fullPath, name string, mode fs.FileMode) {
	if t.files == nil {
		return
	}
	a.fileOp = fileOpState{
		open:  true,
		fresh: true,
		kind:  fileOpPermissions,
		tab:   t,
		path:  fullPath,
		name:  name,
		input: fmt.Sprintf("%04o", mode.Perm()),
	}
}

// validateFileName 检查新建或重命名用的名称。名称里带 / 或者是 ..，
// 会把文件建到或移到别的目录去，这不是用户在这里想做的事。
func validateFileName(name string) error {
	switch {
	case name == "":
		return errors.New("名称不能为空。")
	case name == "." || name == "..":
		return errors.New("名称不能是 . 或 ..。")
	case strings.ContainsAny(name, "/\x00"):
		return errors.New("名称里不能有 /。")
	}
	return nil
}

// submitFileOp 执行新建文件夹、重命名、修改权限。这些都是网络操作，放在后台，
// 期间面板显示处理中，结果回到主线程。
func (a *app) submitFileOp() {
	op := &a.fileOp
	if !op.open || op.busy || op.tab == nil || op.tab.files == nil {
		return
	}
	t, files := op.tab, op.tab.files
	input := strings.TrimSpace(op.input)
	var run func() error
	var done string
	switch op.kind {
	case fileOpNewFolder, fileOpRename:
		if err := validateFileName(input); err != nil {
			op.err = err.Error()
			return
		}
		if op.kind == fileOpNewFolder {
			dir := path.Join(op.path, input)
			run, done = func() error { return files.Mkdir(dir) }, "已创建文件夹 "+input
		} else {
			from, to := op.path, path.Join(path.Dir(op.path), input)
			run, done = func() error { return files.Rename(from, to) }, "已重命名为 "+input
		}
	case fileOpPermissions:
		perm, err := strconv.ParseUint(input, 8, 32)
		if err != nil || perm > 0o7777 {
			op.err = "请输入八进制权限，例如 755 或 0644。"
			return
		}
		target := op.path
		run, done = func() error { return files.Chmod(target, fs.FileMode(perm)) }, "已把权限改为 "+fmt.Sprintf("%04o", perm)
	default:
		return
	}
	op.busy, op.err = true, ""
	go func() {
		err := run()
		a.post(func() {
			op := &a.fileOp
			op.busy = false
			if err != nil {
				op.err = transferReason(err)
				return
			}
			a.fileOp = fileOpState{}
			a.toast(ui.Toast{Title: done})
			if slices.Contains(a.tabs, t) {
				a.openDir(t, t.dir)
			}
		})
	}()
}

func (a *app) askDeleteFile(t *tab, fullPath, name string, isDir bool) {
	a.fileDelete = &fileDeleteState{tab: t, path: fullPath, name: name, isDir: isDir}
}

// confirmDeleteFile 在后台删除文件或空目录。
func (a *app) confirmDeleteFile() {
	del := a.fileDelete
	a.fileDelete = nil
	if del == nil || del.tab == nil || del.tab.files == nil {
		return
	}
	t, files := del.tab, del.tab.files
	go func() {
		var err error
		if del.isDir {
			err = files.RemoveDirectory(del.path)
		} else {
			err = files.Remove(del.path)
		}
		a.post(func() {
			if err != nil {
				reason := transferReason(err)
				if del.isDir && !errors.Is(err, os.ErrPermission) && !errors.Is(err, os.ErrNotExist) {
					reason = "只能删除空文件夹，请先删除里面的文件。"
				}
				a.toast(ui.Toast{Title: "没有删除 " + del.name, Description: reason, Type: "error"})
				return
			}
			a.toast(ui.Toast{Title: "已删除 " + del.name})
			if slices.Contains(a.tabs, t) {
				a.openDir(t, t.dir)
			}
		})
	}()
}

// uploadDropped 把拖到终端上的文件用 SFTP 传到终端的当前目录（shell 通过 OSC 7 上报的目录）。
// 没拿到当前目录时（服务器的 shell 没有上报）传到主目录，并告诉用户。传输进度在文件抽屉里，所以打开它。
func (a *app) uploadDropped(t *tab, p *pane, paths []string) {
	if t.local || t.client == nil {
		return
	}
	var locals, folders []string
	for _, local := range paths {
		info, err := os.Stat(local)
		if err != nil {
			a.toast(ui.Toast{Title: "无法上传 " + filepath.Base(local), Description: err.Error(), Type: "error"})
			continue
		}
		if info.IsDir() {
			folders = append(folders, info.Name())
			continue
		}
		locals = append(locals, local)
	}
	if len(folders) > 0 {
		a.toast(ui.Toast{Title: "暂不支持上传文件夹", Description: "已跳过 " + strings.Join(folders, "、") + "。", Type: "error"})
	}
	if len(locals) == 0 {
		return
	}
	cwd := p.term.Dir()
	a.filesOpen = true
	start := func() {
		if !slices.Contains(a.tabs, t) || t.files == nil {
			return
		}
		if cwd != "" {
			for _, local := range locals {
				a.uploadTo(t, local, path.Join(cwd, filepath.Base(local)))
			}
			return
		}
		files := t.files
		go func() {
			home, err := files.Home()
			a.post(func() {
				if err != nil {
					a.toast(ui.Toast{Title: "无法上传", Description: transferReason(err), Type: "error"})
					return
				}
				a.toast(ui.Toast{Title: "上传到了主目录", Description: "没有拿到终端的当前目录（服务器的 shell 没有上报），文件放在 " + home + "。"})
				for _, local := range locals {
					a.uploadTo(t, local, path.Join(home, filepath.Base(local)))
				}
			})
		}()
	}
	if t.files != nil {
		start()
		return
	}
	t.filesWaiting = append(t.filesWaiting, start)
	a.ensureFiles(t)
}
