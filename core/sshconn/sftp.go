package sshconn

import (
	"cmp"
	"fmt"
	"io/fs"
	"path"
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

// Close 关闭 SFTP 通道，不影响底层 SSH 连接。
func (f *Files) Close() error { return f.client.Close() }
