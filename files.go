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

	"serval/locales"

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
	// folder 表示这是一个文件夹的上传，filesDone/filesTotal 是文件数进度。
	folder     bool
	filesDone  atomic.Int64
	filesTotal atomic.Int64
	err        string
	cancel     context.CancelFunc
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
		a.toast(ui.Toast{Title: locales.FileOpenSaveDialogFailed(), Description: err.Error(), Type: "error"})
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
		a.toast(ui.Toast{Title: locales.FileOpenPickerFailed(), Description: err.Error(), Type: "error"})
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
				a.toast(ui.Toast{Title: locales.FileUploadFailedNamed(fileName), Description: transferReason(err), Type: "error"})
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
	choice := ui.AlertDialog(c, &open, locales.FileReplaceNamed(u.fileName),
		locales.FileReplaceExists(path.Dir(u.remotePath)), locales.FileReplace(), locales.Cancel())
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
			upload := kind == transferUpload
			switch {
			case ctx.Err() != nil:
				tr.err = locales.FileCancelled()
				title := locales.FileDownloadCancelledNamed(fileName)
				if upload {
					title = locales.FileUploadCancelledNamed(fileName)
				}
				a.toast(ui.Toast{Title: title})
			case err != nil:
				tr.err = transferReason(err)
				title := locales.FileDownloadFailedNamed(fileName)
				if upload {
					title = locales.FileUploadFailedNamed(fileName)
				}
				a.toast(ui.Toast{Title: title, Description: tr.err, Type: "error"})
			default:
				title := locales.FileDownloadedNamed(fileName)
				if upload {
					title = locales.FileUploadedNamed(fileName)
				}
				a.toast(ui.Toast{Title: title})
				if upload && slices.Contains(a.tabs, t) {
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
		return locales.FileNoPermission()
	case errors.Is(err, os.ErrNotExist):
		return locales.FileNotExist()
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return locales.FileConnectionLost()
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
		input: locales.NewFolder(),
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
		return errors.New(locales.FileNameEmpty())
	case name == "." || name == "..":
		return errors.New(locales.FileNameDot())
	case strings.ContainsAny(name, "/\x00"):
		return errors.New(locales.FileNameSlash())
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
			run, done = func() error { return files.Mkdir(dir) }, locales.FileFolderCreatedNamed(input)
		} else {
			from, to := op.path, path.Join(path.Dir(op.path), input)
			run, done = func() error { return files.Rename(from, to) }, locales.FileRenamedNamed(input)
		}
	case fileOpPermissions:
		perm, err := strconv.ParseUint(input, 8, 32)
		if err != nil || perm > 0o7777 {
			op.err = locales.FileModeInvalid()
			return
		}
		target := op.path
		run, done = func() error { return files.Chmod(target, fs.FileMode(perm)) }, locales.FileModeChanged(fmt.Sprintf("%04o", perm))
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
					reason = locales.FileDeleteNotEmpty()
				}
				a.toast(ui.Toast{Title: locales.FileNotDeletedNamed(del.name), Description: reason, Type: "error"})
				return
			}
			a.toast(ui.Toast{Title: locales.FileDeletedNamed(del.name)})
			if slices.Contains(a.tabs, t) {
				a.openDir(t, t.dir)
			}
		})
	}()
}

// uploadDropped 把拖到终端上的文件和文件夹用 SFTP 传到终端的当前目录（shell 通过 OSC 7 上报的目录）。
// 没拿到当前目录时（服务器的 shell 没有上报）传到主目录，并告诉用户。传输进度在文件抽屉里，所以打开它。
// 文件夹见 planFolderUpload：远端已有同名文件夹时合并，有同名文件时问一次。
func (a *app) uploadDropped(t *tab, p *pane, paths []string) {
	if t.local || t.client == nil {
		return
	}
	var locals, folders []string
	for _, local := range paths {
		info, err := os.Stat(local)
		if err != nil {
			a.toast(ui.Toast{Title: locales.FileCannotUploadNamed(filepath.Base(local)), Description: err.Error(), Type: "error"})
			continue
		}
		if info.IsDir() {
			folders = append(folders, local)
			continue
		}
		locals = append(locals, local)
	}
	if len(locals) == 0 && len(folders) == 0 {
		return
	}
	cwd := p.term.Dir()
	a.filesOpen = true
	start := func() {
		if !slices.Contains(a.tabs, t) || t.files == nil {
			return
		}
		upload := func(dir string) {
			for _, local := range locals {
				a.uploadTo(t, local, path.Join(dir, filepath.Base(local)))
			}
			for _, folder := range folders {
				a.planFolderUpload(t, folder, dir)
			}
		}
		if cwd != "" {
			upload(cwd)
			return
		}
		files := t.files
		go func() {
			home, err := files.Home()
			a.post(func() {
				if err != nil {
					a.toast(ui.Toast{Title: locales.FileCannotUpload(), Description: transferReason(err), Type: "error"})
					return
				}
				a.toast(ui.Toast{Title: locales.FileUploadedToHome(), Description: locales.FileNoCwd(home)})
				upload(home)
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
