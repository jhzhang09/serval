package main

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"serval/core/sshconn"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// 上传文件夹：远端已有同名文件夹时合并进去；有同名文件时只问一次（全部替换 / 全部跳过 / 取消）。
// 符号链接跳过（跟随可能形成循环，也可能把链接指向的大目录整个传上去）。取消时保留已传完的文件，
// 正在传的那个半截文件由 Files.Upload 删除。

// folderPlan 是一次文件夹上传的计划：要建的目录、要传的文件、和远端冲突的文件。
type folderPlan struct {
	tab        *tab
	localRoot  string
	remoteRoot string
	name       string

	dirs      []string // 相对路径，上级目录在前
	files     []folderFile
	conflicts map[string]bool // 远端已存在的文件（相对路径）
	skipped   []string        // 跳过的符号链接和特殊文件（相对路径）
}

type folderFile struct {
	rel  string // 相对路径，用 / 分隔
	size int64
}

// planFolderUpload 在后台扫描本地文件夹、检查远端，结果回到主线程：有冲突时先问用户，没有就直接上传。
func (a *app) planFolderUpload(t *tab, localRoot, remoteParent string) {
	files := t.files
	name := filepath.Base(localRoot)
	go func() {
		plan, err := scanFolder(localRoot)
		if err == nil {
			plan.tab, plan.name = t, name
			plan.remoteRoot = path.Join(remoteParent, name)
			err = plan.checkRemote(files)
		}
		a.post(func() {
			if !slices.Contains(a.tabs, t) {
				return
			}
			if err != nil {
				a.toast(ui.Toast{Title: locales.FolderCannotUploadNamed(name), Description: transferReason(err), Type: "error"})
				return
			}
			if len(plan.files) == 0 && len(plan.dirs) == 0 {
				a.toast(ui.Toast{Title: locales.FolderNothingToUpload(name)})
				return
			}
			if len(plan.conflicts) > 0 {
				a.pendingFolders = append(a.pendingFolders, plan)
				return
			}
			a.runFolderUpload(plan, false)
		})
	}()
}

// scanFolder 列出本地文件夹里的目录和普通文件，跳过符号链接和设备、管道这类特殊文件。
func scanFolder(root string) (*folderPlan, error) {
	plan := &folderPlan{localRoot: root, conflicts: map[string]bool{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // 读不了的目录：中止并说明原因，不悄悄漏传
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			plan.skipped = append(plan.skipped, rel)
		case d.IsDir():
			plan.dirs = append(plan.dirs, rel)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			plan.files = append(plan.files, folderFile{rel: rel, size: info.Size()})
		default:
			plan.skipped = append(plan.skipped, rel)
		}
		return nil
	})
	return plan, err
}

// checkRemote 找出远端已有的同名文件。只列出远端已经存在的目录（每个目录一次往返），
// 全新的文件夹只需要一次查询。远端同一位置是不同类型（文件对目录）时没法合并，返回错误。
func (plan *folderPlan) checkRemote(files *sshconn.Files) error {
	existing := map[string]bool{} // 远端已存在的目录（相对路径，"" 是文件夹本身）
	entries, err := files.List(path.Dir(plan.remoteRoot))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name == plan.name {
			if !e.IsDir {
				return fmt.Errorf("%s", locales.FolderRemoteNotFolder(path.Dir(plan.remoteRoot), plan.name))
			}
			existing[""] = true
		}
	}
	if !existing[""] {
		return nil // 远端没有这个文件夹：不会有任何冲突
	}
	localDirs, localFiles := map[string]bool{}, map[string]bool{}
	for _, d := range plan.dirs {
		localDirs[d] = true
	}
	for _, f := range plan.files {
		localFiles[f.rel] = true
	}
	dirs := append([]string{""}, plan.dirs...)
	for _, dir := range dirs {
		if !existing[dir] {
			continue // 上级目录在远端不存在，里面自然也没有冲突
		}
		entries, err := files.List(path.Join(plan.remoteRoot, dir))
		if err != nil {
			return err
		}
		for _, e := range entries {
			rel := path.Join(dir, e.Name)
			localIsDir, localIsFile := localDirs[rel], localFiles[rel]
			switch {
			case localIsDir && e.IsDir:
				existing[rel] = true
			case localIsDir || localIsFile && e.IsDir:
				return fmt.Errorf("%s", locales.FolderTypeMismatch(path.Join(plan.remoteRoot, rel)))
			case localIsFile:
				plan.conflicts[rel] = true
			}
		}
	}
	return nil
}

// folderConflictDialog 询问远端已有同名文件时怎么办。默认按钮是“取消”。
func (a *app) folderConflictDialog(c *ui.Context) {
	if len(a.pendingFolders) == 0 {
		return
	}
	plan := a.pendingFolders[0]
	var names []string
	for _, f := range plan.files {
		if plan.conflicts[f.rel] {
			names = append(names, f.rel)
			if len(names) == 3 {
				break
			}
		}
	}
	list := strings.Join(names, locales.FileListSeparator())
	if len(plan.conflicts) > len(names) {
		list += locales.FolderConflictEtc()
	}
	open := true
	choice := ui.AlertDialog(c, &open, locales.FolderConflictsTitle(plan.name, len(plan.conflicts)),
		locales.FolderConflictsBody(plan.remoteRoot, list),
		locales.FolderReplaceAll(), locales.FolderSkipAll(), locales.Cancel())
	if choice == -1 && open {
		return
	}
	a.pendingFolders = a.pendingFolders[1:]
	if !slices.Contains(a.tabs, plan.tab) {
		return
	}
	switch choice {
	case 0:
		a.runFolderUpload(plan, false)
	case 1:
		a.runFolderUpload(plan, true)
	}
}

// runFolderUpload 在后台按计划上传，传输队列里显示成一行，进度按字节和文件数汇总。
func (a *app) runFolderUpload(plan *folderPlan, skipExisting bool) {
	t := plan.tab
	if t.files == nil {
		return
	}
	var todo []folderFile
	var total int64
	for _, f := range plan.files {
		if skipExisting && plan.conflicts[f.rel] {
			continue
		}
		todo = append(todo, f)
		total += f.size
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.nextID++
	tr := &fileTransfer{id: a.nextID, kind: transferUpload, remotePath: plan.remoteRoot, localPath: plan.localRoot,
		fileName: plan.name, folder: true, cancel: cancel}
	tr.total.Store(total)
	tr.filesTotal.Store(int64(len(todo)))
	t.transfers = append(t.transfers, tr)
	files := t.files
	go func() {
		err := uploadFolderFiles(ctx, files, plan, todo, tr)
		a.post(func() {
			defer cancel()
			tr.done.Store(true)
			done := tr.filesDone.Load()
			switch {
			case ctx.Err() != nil:
				tr.err = locales.FileCancelled()
				a.toast(ui.Toast{Title: locales.FolderUploadCancelledNamed(plan.name), Description: locales.FolderUploadKept(int(done))})
			case err != nil:
				tr.err = transferReason(err)
				a.toast(ui.Toast{Title: locales.FolderUploadFailedNamed(plan.name), Description: tr.err, Type: "error"})
			default:
				existing, skipped := len(plan.conflicts), len(plan.skipped)
				var desc string
				switch {
				case skipExisting && existing > 0 && skipped > 0:
					desc = locales.FolderUploadedSkippedBoth(int(done), existing, skipped)
				case skipExisting && existing > 0:
					desc = locales.FolderUploadedSkippedExisting(int(done), existing)
				case skipped > 0:
					desc = locales.FolderUploadedSkippedSpecial(int(done), skipped)
				default:
					desc = locales.FolderUploadedFiles(int(done))
				}
				a.toast(ui.Toast{Title: locales.FolderUploadedNamed(plan.name), Description: desc})
			}
			if slices.Contains(a.tabs, t) {
				a.openDir(t, t.dir)
			}
		})
	}()
}

// uploadFolderFiles 建好目录后逐个上传文件。任何一个失败就停止并报告，不跳过错误继续传。
func uploadFolderFiles(ctx context.Context, files *sshconn.Files, plan *folderPlan, todo []folderFile, tr *fileTransfer) error {
	if err := files.MkdirAll(plan.remoteRoot); err != nil {
		return err
	}
	for _, dir := range plan.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := files.MkdirAll(path.Join(plan.remoteRoot, dir)); err != nil {
			return err
		}
	}
	var finished int64
	lastBytes, lastTime := int64(0), time.Now()
	for _, f := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		local := filepath.Join(plan.localRoot, filepath.FromSlash(f.rel))
		err := files.Upload(ctx, local, path.Join(plan.remoteRoot, f.rel), func(transferred, _ int64) {
			sent := finished + transferred
			tr.transferred.Store(sent)
			if dur := time.Since(lastTime); dur >= 500*time.Millisecond {
				tr.speed.Store((sent - lastBytes) * int64(time.Second) / int64(dur))
				lastBytes, lastTime = sent, time.Now()
			}
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%s", locales.FolderUploadFileFailed(f.rel, transferReason(err)))
		}
		finished += f.size
		tr.transferred.Store(finished)
		tr.filesDone.Add(1)
	}
	return nil
}
