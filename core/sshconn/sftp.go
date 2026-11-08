package sshconn

import (
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

// Upload 把本地文件流式上传到 remotePath。
//
// 先写到服务器上的 remotePath.serval-part，完成后再改名（覆盖同名文件）：
// 中途失败或取消时删除这个临时文件，服务器上不会留下写了一半的文件。
func (f *Files) Upload(ctx context.Context, localPath, remotePath string, progress func(transferred, total int64)) (err error) {
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return fmt.Errorf("获取本地文件信息失败: %w", err)
	}
	part := remotePath + ".serval-part"
	dst, err := f.client.Create(part)
	if err != nil {
		return fmt.Errorf("创建远程文件失败: %w", err)
	}
	defer func() {
		if err != nil {
			dst.Close()
			f.client.Remove(part)
		}
	}()
	if err = copyWithProgress(ctx, dst, src, stat.Size(), progress, "本地文件", "远程文件"); err != nil {
		return err
	}
	if err = dst.Close(); err != nil {
		return fmt.Errorf("写入远程文件失败: %w", err)
	}
	if err = f.client.PosixRename(part, remotePath); err != nil {
		// 不支持 posix-rename 扩展的服务器：先删除旧文件再改名。
		f.client.Remove(remotePath)
		if err = f.client.Rename(part, remotePath); err != nil {
			return fmt.Errorf("保存远程文件失败: %w", err)
		}
	}
	return nil
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
