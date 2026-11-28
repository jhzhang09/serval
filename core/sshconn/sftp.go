package sshconn

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
)

// Entry 是远程目录中的一项。
type Entry struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
}

// Files 是一个 SFTP 通道。
type Files struct {
	client *sftp.Client
}

// OpenFiles 在现有连接上打开 SFTP 通道。
func (c *Client) OpenFiles() (*Files, error) {
	client, err := sftp.NewClient(c.conn)
	if err != nil {
		return nil, fmt.Errorf("打开 SFTP 失败: %w", err)
	}
	return &Files{client: client}, nil
}

// Home 返回远程登录目录。
func (f *Files) Home() (string, error) { return f.client.Getwd() }

// List 列出目录，目录在前，同类按名称排序（不区分大小写）。
func (f *Files) List(dir string) ([]Entry, error) {
	infos, err := f.client.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取目录 %s 失败: %w", dir, err)
	}
	entries := make([]Entry, 0, len(infos))
	for _, info := range infos {
		full := path.Join(dir, info.Name())
		isDir := info.IsDir()
		// ReadDir 不跟随符号链接；指向目录的链接也要能进入，失效的链接按文件显示。
		if info.Mode()&fs.ModeSymlink != 0 {
			if target, err := f.client.Stat(full); err == nil {
				isDir = target.IsDir()
			}
		}
		entries = append(entries, Entry{
			Name:    info.Name(),
			Path:    full,
			IsDir:   isDir,
			Size:    info.Size(),
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
		})
	}
	slices.SortFunc(entries, func(a, b Entry) int {
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

// copyWithProgress 用固定的 64 KiB 缓冲区复制，内存占用不随文件大小增长；每写一块报告一次进度。
func copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader, total int64, progress func(transferred, total int64), readWhat, writeWhat string) error {
	buf := make([]byte, 64*1024)
	var transferred int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return fmt.Errorf("写入%s失败: %w", writeWhat, err)
			}
			transferred += int64(n)
			if progress != nil {
				progress(transferred, total)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("读取%s失败: %w", readWhat, readErr)
		}
	}
}

// Download 把远程文件流式下载到 localPath，目标已有同名文件时覆盖
// （保存对话框里用户已经选过要不要替换）。
//
// 先写到同一目录下的临时文件，完成后再改名：中途失败或取消时删除临时文件，
// 不会留下写了一半的文件，也不会弄坏 localPath 原有的内容。
func (f *Files) Download(ctx context.Context, remotePath, localPath string, progress func(transferred, total int64)) error {
	return f.download(ctx, remotePath, localPath, progress, false)
}

// DownloadIfAbsent 与 Download 相同，但改名落盘前再检查一次目标是否已经存在：
// 从开始下载到落盘之间，别的程序可能创建了同名文件。存在时返回 ErrLocalExists，
// 不覆盖，临时文件删掉。
func (f *Files) DownloadIfAbsent(ctx context.Context, remotePath, localPath string, progress func(transferred, total int64)) error {
	return f.download(ctx, remotePath, localPath, progress, true)
}

func (f *Files) download(ctx context.Context, remotePath, localPath string, progress func(transferred, total int64), guard bool) (err error) {
	src, err := f.client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("打开远程文件失败: %w", err)
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return fmt.Errorf("获取远程文件信息失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(localPath), "."+filepath.Base(localPath)+".*.part")
	if err != nil {
		return fmt.Errorf("创建本地文件失败: %w", err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err = copyWithProgress(ctx, tmp, src, stat.Size(), progress, "远程文件", "本地文件"); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("写入本地文件失败: %w", err)
	}
	if guard {
		if _, statErr := os.Lstat(localPath); statErr == nil {
			return ErrLocalExists
		}
	}
	if err = os.Rename(tmp.Name(), localPath); err != nil {
		return fmt.Errorf("保存本地文件失败: %w", err)
	}
	return nil
}

// FileOwner 指定远端文件或新建目录的属主与属组（UID / GID）。
type FileOwner struct {
	UID int
	GID int
}

// Upload 把本地文件流式上传到 remotePath。
//
// 先写到服务器上的 remotePath.serval-part，完成后再改名（覆盖同名文件）：
// 中途失败或取消时删除这个临时文件，服务器上不会留下写了一半的文件。
func (f *Files) Upload(ctx context.Context, localPath, remotePath string, progress func(transferred, total int64)) error {
	_, err := f.UploadOwned(ctx, localPath, remotePath, nil, progress)
	return err
}

// UploadOwned 与 Upload 相同，但在覆盖同名普通文件时尽量保留原有权限，并在 owner 非空时
// 尝试在改名落盘前将临时文件的属主/属组设置为 owner。若无权限修改属主（例如非 root 登录），
// 文件仍会正常上传，并通过 chownErr 返回改属主失败原因。
func (f *Files) UploadOwned(ctx context.Context, localPath, remotePath string, owner *FileOwner, progress func(transferred, total int64)) (chownErr, err error) {
	src, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return nil, fmt.Errorf("获取本地文件信息失败: %w", err)
	}
	var prevPerm fs.FileMode
	var hasPrevPerm bool
	if existing, lstatErr := f.client.Lstat(remotePath); lstatErr == nil && existing.Mode().IsRegular() {
		prevPerm = existing.Mode().Perm()
		hasPrevPerm = true
	}
	part := remotePath + ".serval-part"
	dst, err := f.client.Create(part)
	if err != nil {
		return nil, fmt.Errorf("创建远程文件失败: %w", err)
	}
	defer func() {
		if err != nil {
			dst.Close()
			f.client.Remove(part)
		}
	}()
	if err = copyWithProgress(ctx, dst, src, stat.Size(), progress, "本地文件", "远程文件"); err != nil {
		return nil, err
	}
	if err = dst.Close(); err != nil {
		return nil, fmt.Errorf("写入远程文件失败: %w", err)
	}
	if owner != nil {
		chownErr = f.client.Chown(part, owner.UID, owner.GID)
	}
	if hasPrevPerm {
		_ = f.client.Chmod(part, prevPerm)
	}
	if err = f.client.PosixRename(part, remotePath); err != nil {
		// 不支持 posix-rename 扩展的服务器：先删除旧文件再改名。
		f.client.Remove(remotePath)
		if err = f.client.Rename(part, remotePath); err != nil {
			return chownErr, fmt.Errorf("保存远程文件失败: %w", err)
		}
	}
	return chownErr, nil
}

// Exists 报告远程路径是否已经存在。
func (f *Files) Exists(remotePath string) (bool, error) {
	_, err := f.client.Stat(remotePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Close 关闭 SFTP 通道，不影响底层 SSH 连接。
func (f *Files) Close() error { return f.client.Close() }

// Mkdir 在远程创建目录。
func (f *Files) Mkdir(remotePath string) error {
	return f.client.Mkdir(remotePath)
}

// MkdirAll 在远程创建目录及缺少的上级目录，目录已存在时不报错。
func (f *Files) MkdirAll(remotePath string) error {
	return f.client.MkdirAll(remotePath)
}

// MkdirAllOwned 创建目录及缺少的上级目录，并对新建的每一级目录尝试设置属主/属组。
// 已存在的上级目录保持原有属主不变。
func (f *Files) MkdirAllOwned(remotePath string, owner *FileOwner) (chownErr, err error) {
	if owner == nil {
		return nil, f.client.MkdirAll(remotePath)
	}
	cleaned := path.Clean(remotePath)
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return nil, nil
	}
	var missing []string
	cur := cleaned
	for cur != "/" && cur != "." && cur != "" {
		if _, statErr := f.client.Lstat(cur); statErr == nil {
			break
		}
		missing = append(missing, cur)
		parent := path.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		dir := missing[i]
		if mkErr := f.client.Mkdir(dir); mkErr != nil {
			if info, statErr := f.client.Stat(dir); statErr == nil && info.IsDir() {
				continue
			}
			return chownErr, mkErr
		}
		if cErr := f.client.Chown(dir, owner.UID, owner.GID); cErr != nil && chownErr == nil {
			chownErr = cErr
		}
	}
	return chownErr, nil
}

// Chown 修改远程文件或目录的属主与属组。
func (f *Files) Chown(remotePath string, uid, gid int) error {
	return f.client.Chown(remotePath, uid, gid)
}

// Rename 重命名或移动远程文件/目录。
func (f *Files) Rename(oldPath, newPath string) error {
	return f.client.Rename(oldPath, newPath)
}

// Remove 删除远程文件。
func (f *Files) Remove(remotePath string) error {
	return f.client.Remove(remotePath)
}

// RemoveDirectory 删除远程空目录。
func (f *Files) RemoveDirectory(remotePath string) error {
	return f.client.RemoveDirectory(remotePath)
}

// Chmod 修改远程文件或目录的权限。
func (f *Files) Chmod(remotePath string, mode fs.FileMode) error {
	return f.client.Chmod(remotePath, mode)
}

// ErrRemoteExists 表示远程路径已经存在同名文件或目录。
var ErrRemoteExists = errors.New("远程已存在同名文件或文件夹")

// CreateEmpty 在远程新建一个空普通文件；如果目标已存在则返回 ErrRemoteExists。
func (f *Files) CreateEmpty(remotePath string) error {
	if _, err := f.client.Lstat(remotePath); err == nil {
		return ErrRemoteExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查远程文件失败: %w", err)
	}
	file, err := f.client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrRemoteExists
		}
		return fmt.Errorf("创建远程文件失败: %w", err)
	}
	return file.Close()
}

// RemoveAll 递归删除远程文件、符号链接或目录。
// 对符号链接只删除链接本身，绝不跟随进入链接指向的目录，防止误删外部目录内容。
func (f *Files) RemoveAll(ctx context.Context, remotePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if remotePath == "" || strings.ContainsRune(remotePath, 0) ||
		strings.HasSuffix(remotePath, "/.") || strings.HasSuffix(remotePath, "/..") {
		return errors.New("无法删除该远程路径")
	}
	cleaned := path.Clean(remotePath)
	base := path.Base(cleaned)
	if cleaned == "/" || cleaned == "." || cleaned == ".." || base == "." || base == ".." {
		return errors.New("无法删除根目录或相对保留路径")
	}
	info, err := f.client.Lstat(cleaned)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", cleaned, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return f.client.Remove(cleaned)
	}
	var walk func(dir string) error
	walk = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		infos, err := f.client.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("读取目录 %s 失败: %w", dir, err)
		}
		for _, child := range infos {
			name := child.Name()
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
				return errors.New("远程文件名不安全，已停止删除")
			}
			childPath := path.Join(dir, name)
			if child.Mode()&fs.ModeSymlink != 0 || !child.IsDir() {
				if err := f.client.Remove(childPath); err != nil {
					return fmt.Errorf("删除 %s 失败: %w", childPath, err)
				}
				continue
			}
			if err := walk(childPath); err != nil {
				return err
			}
		}
		return f.client.RemoveDirectory(dir)
	}
	return walk(cleaned)
}

// 编辑器读写文本时用的哨兵错误。界面负责把它们说成用户能照做的话。
var (
	// ErrFileTooLarge 表示文件比调用方给的上限大，没有把内容读进内存。
	ErrFileTooLarge = errors.New("文件太大")
	// ErrNotText 表示内容含有 NUL 或不是合法 UTF-8，不适合当文本编辑。
	ErrNotText = errors.New("不是文本文件")
	// ErrRemoteChanged 表示保存前发现远端大小或修改时间和打开时不一致。
	ErrRemoteChanged = errors.New("远程文件已变化")
)

// Stat 返回远程路径的大小、修改时间和权限，不跟随符号链接。
func (f *Files) Stat(remotePath string) (size int64, mod time.Time, mode fs.FileMode, err error) {
	info, err := f.client.Lstat(remotePath)
	if err != nil {
		return 0, time.Time{}, 0, fmt.Errorf("读取 %s 失败: %w", remotePath, err)
	}
	return info.Size(), info.ModTime(), info.Mode(), nil
}

// ReadLimited 读取一个普通文本文件。size 大于 limit 时不读正文。
// 符号链接、目录和其他特殊文件直接拒绝，避免改到链接背后的目标。
func (f *Files) ReadLimited(ctx context.Context, remotePath string, limit int64) (data []byte, mod time.Time, err error) {
	if err = ctx.Err(); err != nil {
		return nil, time.Time{}, err
	}
	info, err := f.client.Lstat(remotePath)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("读取 %s 失败: %w", remotePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, time.Time{}, errors.New("只能编辑普通文件")
	}
	if info.Size() > limit {
		return nil, info.ModTime(), ErrFileTooLarge
	}
	src, err := f.client.Open(remotePath)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("打开远程文件失败: %w", err)
	}
	defer src.Close()
	buf := make([]byte, info.Size())
	read := 0
	for read < len(buf) {
		if err = ctx.Err(); err != nil {
			return nil, time.Time{}, err
		}
		n, readErr := src.Read(buf[read:])
		read += n
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, time.Time{}, fmt.Errorf("读取远程文件失败: %w", readErr)
		}
	}
	data = buf[:read]
	if int64(len(data)) != info.Size() {
		return nil, time.Time{}, errors.New("读取文件时大小变了，请再试一次")
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, time.Time{}, ErrNotText
	}
	return data, info.ModTime(), nil
}

// ReadTailLimited 读取普通文本文件末尾最多 limit 字节（文件小于等于 limit 时读取全文）。
// 若从文件中间截断，会自动跳过首段不完整的 UTF-8 字节与残缺首行。
func (f *Files) ReadTailLimited(ctx context.Context, remotePath string, limit int64) (data []byte, mod time.Time, truncated bool, err error) {
	if err = ctx.Err(); err != nil {
		return nil, time.Time{}, false, err
	}
	if limit <= 0 {
		limit = 16 << 10
	}
	info, err := f.client.Lstat(remotePath)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("读取 %s 失败: %w", remotePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, time.Time{}, false, errors.New("只能读取普通文件")
	}
	size := info.Size()
	if size == 0 {
		return nil, info.ModTime(), false, nil
	}
	src, err := f.client.Open(remotePath)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("打开远程文件失败: %w", err)
	}
	defer src.Close()

	offset := int64(0)
	toRead := size
	if size > limit {
		offset = size - limit
		toRead = limit
		truncated = true
		if _, err = src.Seek(offset, io.SeekStart); err != nil {
			return nil, time.Time{}, false, fmt.Errorf("定位远程文件末尾失败: %w", err)
		}
	}
	buf := make([]byte, toRead)
	read := 0
	for read < len(buf) {
		if err = ctx.Err(); err != nil {
			return nil, time.Time{}, false, err
		}
		n, readErr := src.Read(buf[read:])
		read += n
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, time.Time{}, false, fmt.Errorf("读取远程文件失败: %w", readErr)
		}
	}
	data = buf[:read]
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, time.Time{}, false, ErrNotText
	}
	if truncated {
		if nl := bytes.IndexByte(data, '\n'); nl >= 0 && nl+1 < len(data) {
			data = data[nl+1:]
		}
	}
	if !utf8.Valid(data) {
		return nil, time.Time{}, false, ErrNotText
	}
	return data, info.ModTime(), truncated, nil
}

// WriteFile 把 data 写回一个普通文本文件。
// force 为 false 时，远端大小或修改时间（精确到秒）和打开时不一致就返回 ErrRemoteChanged，不写入。
func (f *Files) WriteFile(ctx context.Context, remotePath string, data []byte, expectSize, expectUnix int64, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return ErrNotText
	}
	info, err := f.client.Lstat(remotePath)
	if err != nil {
		return fmt.Errorf("读取远程文件失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("远程路径已经不是普通文件，没有保存")
	}
	if !force && (info.Size() != expectSize || info.ModTime().Unix() != expectUnix) {
		return ErrRemoteChanged
	}
	tmp, err := os.CreateTemp("", "serval-edit-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	return f.Upload(ctx, tmpName, remotePath, nil)
}

// WriteOrCreateFile 原子写入或新建远端普通文本文件，支持可选权限设置与乐观锁校验。
// force 为 false 时，若 expectExists 与远端实际情况不符或文件大小/修改时间发生变化，则返回 ErrRemoteChanged。
func (f *Files) WriteOrCreateFile(ctx context.Context, remotePath string, data []byte, expectExists bool, expectSize, expectUnix int64, mode fs.FileMode, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return ErrNotText
	}
	info, err := f.client.Lstat(remotePath)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return errors.New("远程路径不是普通文件，无法写入")
		}
		if !force && (!expectExists || info.Size() != expectSize || info.ModTime().Unix() != expectUnix) {
			return ErrRemoteChanged
		}
	case errors.Is(err, os.ErrNotExist):
		if !force && expectExists {
			return ErrRemoteChanged
		}
	default:
		return fmt.Errorf("检查远程文件失败: %w", err)
	}
	tmp, err := os.CreateTemp("", "serval-aiwrite-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err = f.Upload(ctx, tmpName, remotePath, nil); err != nil {
		return err
	}
	if mode != 0 {
		if chErr := f.Chmod(remotePath, mode.Perm()); chErr != nil {
			return fmt.Errorf("设置文件权限失败: %w", chErr)
		}
	}
	return nil
}

// ErrLocalExists 表示要把远程目录下载到的本地路径已经存在。调用方应换位置，而不是覆盖。
var ErrLocalExists = errors.New("本地已经有同名的文件或文件夹")

// DownloadTree 把远程目录递归下载到 localRoot。localRoot 必须还不存在，函数会创建它。
//
// 不跟随符号链接，也不下载设备、管道这类特殊文件，它们计入 skipped。
// 中途失败或取消时保留已经下完的文件，不删除 localRoot；正在写的那个文件由 Download 删掉半截临时文件。
// progress 在调用 DownloadTree 的 goroutine 里执行，可以是 nil。不要在里面做界面操作。
func (f *Files) DownloadTree(ctx context.Context, remoteRoot, localRoot string, progress func(doneFiles, totalFiles, doneBytes, totalBytes int64)) (skipped int, err error) {
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	dirs, treeFiles, skippedPaths, err := f.readTree(remoteRoot)
	if err != nil {
		return 0, err
	}
	if _, err = os.Lstat(localRoot); err == nil {
		return 0, ErrLocalExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("检查本地文件夹失败: %w", err)
	}
	if err = os.MkdirAll(localRoot, 0o755); err != nil {
		return 0, fmt.Errorf("创建本地文件夹失败: %w", err)
	}
	var totalBytes int64
	for _, file := range treeFiles {
		totalBytes += file.size
	}
	totalFiles := int64(len(treeFiles))
	if progress != nil {
		progress(0, totalFiles, 0, totalBytes)
	}
	for _, dir := range dirs {
		if err = ctx.Err(); err != nil {
			return len(skippedPaths), err
		}
		dest, destErr := localChild(localRoot, dir)
		if destErr != nil {
			return len(skippedPaths), destErr
		}
		if err = os.MkdirAll(dest, 0o755); err != nil {
			return len(skippedPaths), fmt.Errorf("创建本地文件夹失败: %w", err)
		}
	}
	var doneBytes, doneFiles int64
	for _, file := range treeFiles {
		if err = ctx.Err(); err != nil {
			return len(skippedPaths), err
		}
		dest, destErr := localChild(localRoot, file.rel)
		if destErr != nil {
			return len(skippedPaths), destErr
		}
		remoteFile := path.Join(remoteRoot, file.rel)
		err = f.Download(ctx, remoteFile, dest, func(transferred, _ int64) {
			if progress != nil {
				progress(doneFiles, totalFiles, doneBytes+transferred, totalBytes)
			}
		})
		if err != nil {
			if ctx.Err() != nil {
				return len(skippedPaths), ctx.Err()
			}
			return len(skippedPaths), fmt.Errorf("%s: %w", file.rel, err)
		}
		doneBytes += file.size
		doneFiles++
		if progress != nil {
			progress(doneFiles, totalFiles, doneBytes, totalBytes)
		}
	}
	return len(skippedPaths), nil
}

type treeFile struct {
	rel  string
	size int64
}

// readTree 列出远程目录里的子目录和普通文件。符号链接和特殊文件放进 skipped，不跟随。
func (f *Files) readTree(remoteRoot string) (dirs []string, files []treeFile, skipped []string, err error) {
	info, err := f.client.Lstat(remoteRoot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("读取 %s 失败: %w", remoteRoot, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, nil, errors.New("这是符号链接，没有下载")
	}
	if !info.IsDir() {
		return nil, nil, nil, fmt.Errorf("%s 不是文件夹", remoteRoot)
	}
	var walk func(rel string) error
	walk = func(rel string) error {
		full := remoteRoot
		if rel != "" {
			full = path.Join(remoteRoot, rel)
		}
		infos, err := f.client.ReadDir(full)
		if err != nil {
			return fmt.Errorf("读取目录 %s 失败: %w", full, err)
		}
		for _, info := range infos {
			name := info.Name()
			if err := checkRemoteName(name); err != nil {
				return err
			}
			child := name
			if rel != "" {
				child = path.Join(rel, name)
			}
			mode := info.Mode()
			switch {
			case mode&fs.ModeSymlink != 0:
				skipped = append(skipped, child)
			case mode.IsDir():
				dirs = append(dirs, child)
				if err := walk(child); err != nil {
					return err
				}
			case mode.IsRegular():
				files = append(files, treeFile{rel: child, size: info.Size()})
			default:
				skipped = append(skipped, child)
			}
		}
		return nil
	}
	if err = walk(""); err != nil {
		return nil, nil, nil, err
	}
	return dirs, files, skipped, nil
}

// checkRemoteName 拒绝会逃出目标目录的文件名。名字来自服务器，不能当成本地路径的一段来信任。
func checkRemoteName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return errors.New("远程文件名不安全，没有下载")
	}
	return nil
}

// localChild 把远程相对路径映射到 localRoot 里面。相对路径里就算混进了 .. 也不许出去。
func localChild(localRoot, rel string) (string, error) {
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
		return "", errors.New("远程文件名不安全，没有下载")
	}
	target := filepath.Join(localRoot, filepath.FromSlash(cleaned))
	relLocal, err := filepath.Rel(localRoot, target)
	if err != nil || relLocal == ".." || strings.HasPrefix(relLocal, ".."+string(filepath.Separator)) {
		return "", errors.New("远程文件名不安全，没有下载")
	}
	return target, nil
}
