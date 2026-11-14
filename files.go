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

	"serval/core/sshconn"
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
	// folder 表示这一行是整个文件夹的上传或下载，filesDone/filesTotal 是文件数进度。
	folder     bool
	filesDone  atomic.Int64
	filesTotal atomic.Int64
	err        string
	guardLocal bool
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
	a.runTransfer(t, transferDownload, remotePath, localPath, fileName, false)
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

// startUploadFolder 选一个本地文件夹，上传到抽屉当前目录。
// 远端已有同名文件夹时的合并和确认，与拖到终端上的文件夹相同。
func (a *app) startUploadFolder(t *tab) {
	if t.files == nil || t.dir == "" || t.local {
		return
	}
	if a.chooseUploadFolder == nil {
		a.toast(ui.Toast{Title: "无法打开文件夹选择框", Description: "这个版本没有接上文件夹选择。", Type: "error"})
		return
	}
	local, err := a.chooseUploadFolder()
	if err != nil {
		a.toast(ui.Toast{Title: "无法打开文件夹选择框", Description: err.Error(), Type: "error"})
		return
	}
	if local == "" {
		return
	}
	info, err := os.Stat(local)
	if err != nil || !info.IsDir() {
		a.toast(ui.Toast{Title: "请选择一个文件夹", Type: "error"})
		return
	}
	a.planFolderUpload(t, local, t.dir)
}

// startDownloadFolder 把远程文件夹下载到用户选的目录里，保存为同名文件夹。
func (a *app) startDownloadFolder(t *tab, remotePath, name string) {
	if t.files == nil || t.local {
		return
	}
	if err := validateFileName(name); err != nil {
		a.toast(ui.Toast{Title: "无法下载这个文件夹", Description: err.Error(), Type: "error"})
		return
	}
	if a.chooseDownloadFolder == nil {
		a.toast(ui.Toast{Title: "无法打开文件夹选择框", Description: "这个版本没有接上文件夹选择。", Type: "error"})
		return
	}
	parent, err := a.chooseDownloadFolder()
	if err != nil {
		a.toast(ui.Toast{Title: "无法打开文件夹选择框", Description: err.Error(), Type: "error"})
		return
	}
	if parent == "" {
		return
	}
	a.runFolderDownload(t, remotePath, filepath.Join(parent, name), name)
}

// runFolderDownload 在后台递归下载文件夹。取消时保留已经下完的文件。
func (a *app) runFolderDownload(t *tab, remoteRoot, localRoot, name string) {
	if t.files == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.nextID++
	tr := &fileTransfer{id: a.nextID, kind: transferDownload, remotePath: remoteRoot, localPath: localRoot,
		fileName: name, folder: true, cancel: cancel}
	t.transfers = append(t.transfers, tr)
	files := t.files
	go func() {
		lastBytes, lastTime := int64(0), time.Now()
		skipped, err := files.DownloadTree(ctx, remoteRoot, localRoot, func(doneFiles, totalFiles, doneBytes, totalBytes int64) {
			tr.filesDone.Store(doneFiles)
			tr.filesTotal.Store(totalFiles)
			tr.transferred.Store(doneBytes)
			tr.total.Store(totalBytes)
			if dur := time.Since(lastTime); dur >= 500*time.Millisecond {
				tr.speed.Store((doneBytes - lastBytes) * int64(time.Second) / int64(dur))
				lastBytes, lastTime = doneBytes, time.Now()
			}
		})
		a.post(func() {
			defer cancel()
			tr.done.Store(true)
			done := tr.filesDone.Load()
			switch {
			case ctx.Err() != nil:
				tr.err = "已取消"
				a.toast(ui.Toast{Title: "已取消下载文件夹 " + name, Description: fmt.Sprintf("已下完的 %d 个文件留在本地。", done)})
			case errors.Is(err, sshconn.ErrLocalExists):
				tr.err = "本地已经有同名的文件或文件夹"
				a.toast(ui.Toast{Title: "没有下载文件夹 " + name, Description: "本地已经有同名的文件或文件夹，请换一个位置，或先把原来的移走。", Type: "error"})
			case err != nil:
				tr.err = transferReason(err)
				a.toast(ui.Toast{Title: "下载文件夹 " + name + " 失败", Description: tr.err, Type: "error"})
			default:
				desc := fmt.Sprintf("%d 个文件", done)
				if skipped > 0 {
					desc += fmt.Sprintf("，跳过了 %d 个符号链接或特殊文件", skipped)
				}
				a.toast(ui.Toast{Title: "已下载文件夹 " + name, Description: desc + "。"})
				if a.localDir != "" {
					a.openLocal(a.localDir)
				}
			}
		})
	}()
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
			a.runTransfer(t, transferUpload, remotePath, localPath, fileName, false)
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
		a.runTransfer(u.tab, transferUpload, u.remotePath, u.localPath, u.fileName, false)
	}
}

// runTransfer 在后台执行一次传输，进度通过原子字段给界面读取，结束后回到主线程提示结果。
// guardLocal 为真时，下载落盘前再检查一次本地同名文件，存在就不覆盖（拖放下载承诺不覆盖）。
func (a *app) runTransfer(t *tab, kind transferKind, remotePath, localPath, fileName string, guardLocal bool) {
	if t.files == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.nextID++
	tr := &fileTransfer{id: a.nextID, kind: kind, remotePath: remotePath, localPath: localPath, fileName: fileName, guardLocal: guardLocal, cancel: cancel}
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
		switch {
		case kind == transferDownload && guardLocal:
			err = files.DownloadIfAbsent(ctx, remotePath, localPath, progress)
		case kind == transferDownload:
			err = files.Download(ctx, remotePath, localPath, progress)
		default:
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
			case errors.Is(err, sshconn.ErrLocalExists):
				tr.err = "本地已经有同名的文件或文件夹"
				a.toast(ui.Toast{Title: "没有下载 " + fileName, Description: "下载期间本地出现了同名的文件或文件夹，没有覆盖它，请先把原来的移走。", Type: "error"})
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
				if kind == transferDownload && a.localDir != "" {
					a.openLocal(a.localDir)
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

func (a *app) dismissTransfer(t *tab, id int) {
	if t == nil {
		return
	}
	t.transfers = slices.DeleteFunc(t.transfers, func(tr *fileTransfer) bool {
		return tr.id == id && tr.done.Load()
	})
}

func (a *app) clearFinishedTransfers(t *tab) {
	if t == nil {
		return
	}
	t.transfers = slices.DeleteFunc(t.transfers, func(tr *fileTransfer) bool {
		return tr.done.Load()
	})
}

func (a *app) retryTransfer(t *tab, item *fileTransfer) {
	if t == nil || item == nil || !item.done.Load() || item.err == "" || t.files == nil {
		return
	}
	a.dismissTransfer(t, item.id)
	if item.folder {
		if item.kind == transferDownload {
			a.runFolderDownload(t, item.remotePath, item.localPath, item.fileName)
		} else {
			a.planFolderUpload(t, item.localPath, path.Dir(item.remotePath))
		}
		return
	}
	if item.kind == transferDownload {
		a.runTransfer(t, transferDownload, item.remotePath, item.localPath, item.fileName, item.guardLocal)
		return
	}
	a.runTransfer(t, transferUpload, item.remotePath, item.localPath, item.fileName, false)
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
	local bool
	tab   *tab
	path  string
	name  string
	input string
	err   string
	// busy 表示操作正在后台执行，期间按钮禁用。
	busy bool
}

type fileDeleteState struct {
	local bool
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

func (a *app) openLocalNewFolder() {
	if a.localDir == "" {
		return
	}
	a.fileOp = fileOpState{
		open:  true,
		fresh: true,
		kind:  fileOpNewFolder,
		local: true,
		path:  a.localDir,
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

func (a *app) openLocalRenameFile(fullPath, name string) {
	if a.localDir == "" || fullPath == "" {
		return
	}
	a.fileOp = fileOpState{
		open:  true,
		fresh: true,
		kind:  fileOpRename,
		local: true,
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

// submitFileOp 执行新建文件夹、重命名、修改权限。网络操作或本地磁盘操作放在后台，
// 期间面板显示处理中，结果回到主线程。
func (a *app) submitFileOp() {
	op := &a.fileOp
	if !op.open || op.busy {
		return
	}
	if !op.local && (op.tab == nil || op.tab.files == nil) {
		return
	}
	t := op.tab
	isLocal := op.local
	input := strings.TrimSpace(op.input)
	var run func() error
	var done string
	switch op.kind {
	case fileOpNewFolder, fileOpRename:
		if err := validateFileName(input); err != nil {
			op.err = err.Error()
			return
		}
		if isLocal {
			if op.kind == fileOpNewFolder {
				dir := filepath.Join(op.path, input)
				run, done = func() error { return os.Mkdir(dir, 0o755) }, locales.FileFolderCreatedNamed(input)
			} else {
				from, to := op.path, filepath.Join(filepath.Dir(op.path), input)
				run, done = func() error {
					if from == to {
						return nil
					}
					if _, err := os.Lstat(to); err == nil {
						return errors.New("目标名称已存在")
					}
					return os.Rename(from, to)
				}, locales.FileRenamedNamed(input)
			}
		} else {
			files := t.files
			if op.kind == fileOpNewFolder {
				dir := path.Join(op.path, input)
				run, done = func() error { return files.Mkdir(dir) }, locales.FileFolderCreatedNamed(input)
			} else {
				from, to := op.path, path.Join(path.Dir(op.path), input)
				run, done = func() error { return files.Rename(from, to) }, locales.FileRenamedNamed(input)
			}
		}
	case fileOpPermissions:
		if isLocal || t == nil || t.files == nil {
			return
		}
		files := t.files
		target := op.path
		perm, err := strconv.ParseUint(input, 8, 32)
		if err != nil || perm > 0o7777 {
			op.err = locales.FileModeInvalid()
			return
		}
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
			if isLocal {
				if a.localDir != "" {
					a.openLocal(a.localDir)
				}
				return
			}
			if slices.Contains(a.tabs, t) {
				a.openDir(t, t.dir)
			}
		})
	}()
}

func (a *app) askDeleteFile(t *tab, fullPath, name string, isDir bool) {
	a.fileDelete = &fileDeleteState{tab: t, path: fullPath, name: name, isDir: isDir}
}

func (a *app) askDeleteLocalFile(fullPath, name string, isDir bool) {
	a.fileDelete = &fileDeleteState{local: true, path: fullPath, name: name, isDir: isDir}
}

// confirmDeleteFile 在后台删除文件或空目录（支持本地与远程）。
func (a *app) confirmDeleteFile() {
	del := a.fileDelete
	a.fileDelete = nil
	if del == nil || (!del.local && (del.tab == nil || del.tab.files == nil)) {
		return
	}
	t := del.tab
	isLocal := del.local
	go func() {
		var err error
		if isLocal {
			err = os.Remove(del.path)
		} else {
			files := t.files
			if del.isDir {
				err = files.RemoveDirectory(del.path)
			} else {
				err = files.Remove(del.path)
			}
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
			if isLocal {
				if a.localDir != "" {
					a.openLocal(a.localDir)
				}
				return
			}
			if slices.Contains(a.tabs, t) {
				a.openDir(t, t.dir)
			}
		})
	}()
}

// classifyDrop 把拖进来的路径分成普通文件和文件夹。读不到的路径当场说明，不中断其余文件。
func (a *app) classifyDrop(paths []string) (files, folders []string) {
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
		files = append(files, local)
	}
	return files, folders
}

// uploadPaths 把已经分好类的本地路径上传到远程 dir。
func (a *app) uploadPaths(t *tab, dir string, files, folders []string) {
	if !slices.Contains(a.tabs, t) || t.files == nil || dir == "" {
		return
	}
	for _, local := range files {
		a.uploadTo(t, local, path.Join(dir, filepath.Base(local)))
	}
	for _, folder := range folders {
		a.planFolderUpload(t, folder, dir)
	}
}

// uploadInto 把拖进来的文件和文件夹上传到远程 dir（文件抽屉的当前目录）。
func (a *app) uploadInto(t *tab, dir string, paths []string) {
	files, folders := a.classifyDrop(paths)
	if len(files) == 0 && len(folders) == 0 {
		return
	}
	a.uploadPaths(t, dir, files, folders)
}

// uploadDropped 把拖到终端上的文件和文件夹用 SFTP 传到终端的当前目录（shell 通过 OSC 7 上报的目录）。
// 没拿到当前目录时（服务器的 shell 没有上报）传到主目录，并告诉用户。传输进度在文件抽屉里，所以打开它。
// 文件夹见 planFolderUpload：远端已有同名文件夹时合并，有同名文件时问一次。
func (a *app) uploadDropped(t *tab, p *pane, paths []string) {
	if t.local || t.client == nil {
		return
	}
	files, folders := a.classifyDrop(paths)
	if len(files) == 0 && len(folders) == 0 {
		return
	}
	cwd := ""
	if p != nil && p.term != nil {
		cwd = p.term.Dir()
	}
	a.filesOpen = true
	start := func() {
		if !slices.Contains(a.tabs, t) || t.files == nil {
			return
		}
		if cwd != "" {
			a.uploadPaths(t, cwd, files, folders)
			return
		}
		remote := t.files
		go func() {
			home, err := remote.Home()
			a.post(func() {
				if err != nil {
					a.toast(ui.Toast{Title: locales.FileCannotUpload(), Description: transferReason(err), Type: "error"})
					return
				}
				a.toast(ui.Toast{Title: locales.FileUploadedToHome(), Description: locales.FileNoCwd(home)})
				a.uploadPaths(t, home, files, folders)
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
