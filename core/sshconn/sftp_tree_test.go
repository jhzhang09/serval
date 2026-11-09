package sshconn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"serval/internal/devsshd"
)

func TestDownloadTreeCopiesFilesAndSkipsLinks(t *testing.T) {
	remote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remote, "sub", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "sub", "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(remote, "a.txt"), filepath.Join(remote, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(remote, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	files, err := client.OpenFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	local := filepath.Join(t.TempDir(), "pack")
	skipped, err := files.DownloadTree(context.Background(), remote, local, nil)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 2 {
		t.Fatalf("应跳过符号链接和管道，skipped=%d", skipped)
	}
	if got := readLocal(t, filepath.Join(local, "a.txt")); got != "aaa" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := readLocal(t, filepath.Join(local, "sub", "b.txt")); got != "bbb" {
		t.Fatalf("b.txt = %q", got)
	}
	if info, err := os.Stat(filepath.Join(local, "sub", "empty")); err != nil || !info.IsDir() {
		t.Fatal("空目录也应建好")
	}
	if _, err := os.Lstat(filepath.Join(local, "link.txt")); err == nil {
		t.Fatal("符号链接不应下载")
	}
	if _, err := os.Lstat(filepath.Join(local, "pipe")); err == nil {
		t.Fatal("管道不应下载")
	}

	again := filepath.Join(t.TempDir(), "pack")
	os.MkdirAll(again, 0o755)
	if _, err := files.DownloadTree(context.Background(), remote, again, nil); !errors.Is(err, ErrLocalExists) {
		t.Fatalf("本地已存在时应拒绝覆盖，err=%v", err)
	}
}

func TestDownloadTreeCancelKeepsFinishedFiles(t *testing.T) {
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "b.txt"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	files, err := client.OpenFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := filepath.Join(t.TempDir(), "out")
	_, err = files.DownloadTree(ctx, remote, local, func(doneFiles, _, _, _ int64) {
		if doneFiles >= 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后应返回 context.Canceled，err=%v", err)
	}
	finished := 0
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(local, name)); err == nil {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("应留下已下完的 1 个文件，实际 %d 个", finished)
	}
	if parts := partFiles(t, local); len(parts) != 0 {
		t.Fatalf("不应留下半截文件: %v", parts)
	}
}

func TestDownloadTreeRejectsFileAndMissing(t *testing.T) {
	remote := t.TempDir()
	file := filepath.Join(remote, "one.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	files, err := client.OpenFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	local := filepath.Join(t.TempDir(), "out")
	if _, err := files.DownloadTree(context.Background(), file, local, nil); err == nil {
		t.Fatal("对文件调用 DownloadTree 应失败")
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatal("失败时不应创建本地目录")
	}
	if _, err := files.DownloadTree(context.Background(), filepath.Join(remote, "missing"), local, nil); err == nil {
		t.Fatal("不存在的目录应失败")
	}
}

func readLocal(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func partFiles(t *testing.T, root string) []string {
	t.Helper()
	var parts []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".part") {
			parts = append(parts, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return parts
}

// 回归：DownloadIfAbsent 落盘前检查目标，已存在时返回 ErrLocalExists 而不是覆盖。
func TestDownloadIfAbsentRefusesExisting(t *testing.T) {
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := startServer(t, devsshd.Options{})
	client := mustDial(t, dialConfig(server, acceptAll(t)))
	files, err := client.OpenFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := files.DownloadIfAbsent(context.Background(), filepath.Join(remote, "a.txt"), target, nil); err != nil {
		t.Fatal(err)
	}
	// 目标在下载开始后才出现的场景：落盘前检查到它，拒绝覆盖。
	if err := os.WriteFile(target, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := files.DownloadIfAbsent(context.Background(), filepath.Join(remote, "a.txt"), target, nil); !errors.Is(err, ErrLocalExists) {
		t.Fatalf("目标已存在时应返回 ErrLocalExists，err=%v", err)
	}
	if got := readLocal(t, target); got != "local" {
		t.Fatalf("不应覆盖本地内容，= %q", got)
	}
	// 拒绝后不应留下写了一半的临时文件。
	matches, _ := filepath.Glob(filepath.Join(dir, ".a.txt.*.part"))
	if len(matches) != 0 {
		t.Fatalf("应清掉临时文件: %v", matches)
	}
}
