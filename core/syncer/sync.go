// Package syncer 把本机保险库和远端的一份 vault.json 对齐。
// 传输方式只依赖 Remote：下载、按 ETag 条件上传。WebDAV 是其中一种实现。
package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

const maxConflictRetries = 3

// Vault 是同步要读写的本机保险库。*vault.Vault 满足这个接口。
type Vault interface {
	// Merge 把远端文件合并进本机。远端更新、或本机有更新，都由调用方再决定要不要上传。
	Merge(remote []byte) (changed bool, err error)
	// Bytes 返回当前保险库文件，用来上传。
	Bytes() ([]byte, error)
}

// Remote 是一份远端的 vault.json。
type Remote interface {
	// Download 返回文件内容和 ETag。文件不存在时返回 ErrNotFound。
	Download(ctx context.Context) (body []byte, etag string, err error)
	// Upload 上传文件。ifMatch 为空表示远端应该还没有这个文件（If-None-Match: *）；
	// 非空时必须仍然是这个 ETag（If-Match）。版本对不上返回 ErrConflict。
	Upload(ctx context.Context, body []byte, ifMatch string) (etag string, err error)
}

// Sync 下载远端、合并、再把合并结果上传。
// 上传时如果 ETag 已经变了，重新下载合并再上传，最多重试 maxConflictRetries 次。
// 网络失败时本机文件保持原样（合并发生在下载成功之后；上传失败不会回滚已经合并进来的远端修改）。
func Sync(ctx context.Context, v Vault, remote Remote) error {
	var conflict error
	for attempt := 0; attempt <= maxConflictRetries; attempt++ {
		err := syncOnce(ctx, v, remote)
		if !errors.Is(err, ErrConflict) {
			return err
		}
		conflict = err
	}
	return fmt.Errorf("同步时和其他设备改到了一起，已重试 %d 次仍未成功: %w", maxConflictRetries, conflict)
}

func syncOnce(ctx context.Context, v Vault, remote Remote) error {
	body, etag, err := remote.Download(ctx)
	if errors.Is(err, ErrNotFound) {
		local, err := v.Bytes()
		if err != nil {
			return err
		}
		_, err = remote.Upload(ctx, local, "")
		return err
	}
	if err != nil {
		return err
	}
	if _, err := v.Merge(body); err != nil {
		return err
	}
	local, err := v.Bytes()
	if err != nil {
		return err
	}
	if bytes.Equal(local, body) {
		return nil
	}
	_, err = remote.Upload(ctx, local, etag)
	return err
}
