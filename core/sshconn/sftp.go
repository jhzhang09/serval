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

// Download 把远程文件流式下载到 localPath。
//
// 先写到同一目录下的临时文件，完成后再改名：中途失败或取消时删除临时文件，
// 不会留下写了一半的文件，也不会弄坏 localPath 原有的内容。
func (f *Files) Download(ctx context.Context, remotePath, localPath string, progress func(transferred, total int64)) (err error) {
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
