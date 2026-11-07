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
