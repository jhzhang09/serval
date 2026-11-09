package vault

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func init() {
	// 测试里用很低的派生成本，正式参数见 DefaultKDF。
	DefaultKDF = KDF{Alg: "argon2id", Time: 1, MemoryKiB: 1024, Threads: 1}
}

const password = "correct horse"

func newVault(t *testing.T) (*Vault, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.json")
	v, err := Open(path, "dev1")
	if err != nil {
		t.Fatal(err)
	}
	return v, path
}

func TestUnprotectedRoundTrip(t *testing.T) {
	v, path := newVault(t)
	if v.State() != Unprotected {
		t.Fatalf("新保险库应是未加密状态，实际 %v", v.State())
	}
	if err := v.Put("host", "h1", []byte(`{"name":"web"}`)); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path, "dev1")
	if err != nil {
		t.Fatal(err)
	}
	list, err := again.List("host")
	if err != nil || len(list) != 1 || string(list[0].Data) != `{"name":"web"}` {
		t.Fatalf("list=%+v err=%v", list, err)
	}
}

func TestProtectEncryptsEverything(t *testing.T) {
	v, path := newVault(t)
	v.Put("host", "h1", []byte(`{"name":"web.internal","password":"hunter2"}`))
	if err := v.Protect("short"); err == nil {
		t.Fatal("过短的主密码应被拒绝")
	}
	if err := v.Protect(password); err != nil {
		t.Fatal(err)
	}
	if v.State() != Unlocked {
		t.Fatalf("设置主密码后应保持解锁，实际 %v", v.State())
	}
	raw, _ := os.ReadFile(path)
	for _, leak := range []string{"web.internal", "hunter2", password} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("文件里不应出现明文 %q", leak)
		}
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("文件权限 = %v", info.Mode().Perm())
		}
	}
	if err := v.Protect(password); !errors.Is(err, ErrAlreadyProtected) {
		t.Errorf("重复设置应报错: %v", err)
	}

	locked, err := Open(path, "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if locked.State() != Locked {
		t.Fatalf("重新打开应是锁定状态")
	}
	if _, err := locked.List("host"); !errors.Is(err, ErrLocked) {
		t.Fatalf("锁定时不应能读取: %v", err)
	}
	if err := locked.Put("host", "x", nil); !errors.Is(err, ErrLocked) {
		t.Fatalf("锁定时不应能写入: %v", err)
	}
	if err := locked.Unlock("wrong password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("err=%v", err)
	}
	if err := locked.Unlock(password); err != nil {
		t.Fatal(err)
	}
	list, _ := locked.List("host")
	if len(list) != 1 || !strings.Contains(string(list[0].Data), "hunter2") {
		t.Fatalf("解锁后应能读到原文: %+v", list)
	}
}

func TestRememberedKeyUnlocks(t *testing.T) {
	v, path := newVault(t)
	v.Protect(password)
	v.Put("key", "k1", []byte("secret"))
	key := v.Key()
	v.Lock()
	if v.Key() != nil || v.State() != Locked {
		t.Fatal("锁定后应丢掉密钥")
	}
	again, _ := Open(path, "dev1")
	if err := again.UnlockWithKey([]byte("0123456789abcdef0123456789abcdef")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("错误的密钥应解锁失败: %v", err)
	}
	if err := again.UnlockWithKey(key); err != nil {
		t.Fatal(err)
	}
	if list, _ := again.List("key"); len(list) != 1 || string(list[0].Data) != "secret" {
		t.Fatalf("list=%+v", list)
	}
}

// 篡改或者把一条记录的密文挪到另一条记录下，都必须解密失败。
func TestTamperingIsDetected(t *testing.T) {
	v, path := newVault(t)
	v.Protect(password)
	v.Put("host", "a", []byte(`{"name":"a"}`))
	v.Put("host", "b", []byte(`{"name":"b"}`))

	var f file
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &f)
	f.Records[0].Data, f.Records[1].Data = f.Records[1].Data, f.Records[0].Data
	swapped, _ := json.Marshal(f)
	os.WriteFile(path, swapped, 0o600)

	again, _ := Open(path, "dev1")
	if err := again.Unlock(password); err == nil {
		t.Fatal("交换过密文的保险库不应能解锁")
	}
}

func TestChangePasswordKeepsData(t *testing.T) {
	v, path := newVault(t)
	v.Protect(password)
	v.Put("host", "h", []byte("x"))
	before := v.Key()
	if err := v.ChangePassword("nope nope", "new password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("err=%v", err)
	}
	if err := v.ChangePassword(password, "new password"); err != nil {
		t.Fatal(err)
	}
	again, _ := Open(path, "dev1")
	if again.Unlock(password) == nil {
		t.Fatal("旧主密码不应再能解锁")
	}
	if err := again.Unlock("new password"); err != nil {
		t.Fatal(err)
	}
	if string(again.Key()) != string(before) {
		t.Error("改主密码不应更换保险库密钥（否则其他设备记住的密钥会失效，同步也要重新加密）")
	}
}

func TestDeleteLeavesTombstone(t *testing.T) {
	v, path := newVault(t)
	v.Put("host", "h", []byte("x"))
	if err := v.Delete("h"); err != nil {
		t.Fatal(err)
	}
	if list, _ := v.List("host"); len(list) != 0 {
		t.Fatalf("删除后不应再列出: %+v", list)
	}
	var f file
	raw, _ := os.ReadFile(path)
	json.Unmarshal(raw, &f)
	if len(f.Records) != 1 || !f.Records[0].Deleted || f.Records[0].Data != "" {
		t.Fatalf("应留下不含内容的墓碑: %+v", f.Records)
	}
}

func TestRevIncreases(t *testing.T) {
	v, _ := newVault(t)
	v.Put("host", "h", []byte("1"))
	first, _ := v.List("host")
	v.Put("host", "h", []byte("2"))
	second, _ := v.List("host")
	if !(second[0].Rev > first[0].Rev) || !strings.HasSuffix(second[0].Rev, "-dev1") {
		t.Fatalf("rev 应递增并带设备 ID: %s → %s", first[0].Rev, second[0].Rev)
	}
	// 时钟回拨：旧 rev 在未来，新的 rev 仍然更大。
	future := "0009999999999999-0005-dev2"
	if got := v.nextRev(future); !(got > future) {
		t.Fatalf("时钟回拨时 rev = %s", got)
	}
}

// 写文件失败时内存里的状态不变，不会出现“界面显示已保存、文件里却没有”。
func TestFailedSaveRollsBack(t *testing.T) {
	dir := t.TempDir()
	v, err := Open(filepath.Join(dir, "sub", "vault.json"), "d")
	if err != nil {
		t.Fatal(err)
	}
	v.Put("host", "keep", []byte("1"))
	os.Chmod(filepath.Join(dir, "sub"), 0o500)
	defer os.Chmod(filepath.Join(dir, "sub"), 0o700)
	if err := v.Apply([]Change{{Kind: "host", ID: "new", Data: []byte("2")}, {ID: "keep", Delete: true}}); err == nil {
		t.Skip("当前用户可以写只读目录（可能是 root），跳过")
	}
	list, _ := v.List("host")
	if len(list) != 1 || list[0].ID != "keep" {
		t.Fatalf("保存失败后应回滚: %+v", list)
	}
}

func TestDeviceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-id")
	a, err := DeviceID(path)
	if err != nil || len(a) != 16 {
		t.Fatalf("a=%q err=%v", a, err)
	}
	if b, _ := DeviceID(path); b != a {
		t.Fatal("设备 ID 应保持不变")
	}
}

func TestNewerFormatIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.json")
	os.WriteFile(path, []byte(`{"format":99,"records":[]}`), 0o600)
	if _, err := Open(path, "d"); err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("err=%v", err)
	}
}

// 派生密钥（Argon2id）在锁外进行：Protect、Unlock 进行中，State() 不应等待。
// 这里用正式参数，测试里调低的参数太快，测不出等锁。
func TestKDFDoesNotHoldLock(t *testing.T) {
	old := DefaultKDF
	DefaultKDF = KDF{Alg: "argon2id", Time: 3, MemoryKiB: 64 * 1024, Threads: 4}
	defer func() { DefaultKDF = old }()
	v, path := newVault(t)

	measure := func(op func() error) (time.Duration, time.Duration) {
		done := make(chan time.Duration, 1)
		go func() {
			s := time.Now()
			if err := op(); err != nil {
				t.Error(err)
			}
			done <- time.Since(s)
		}()
		time.Sleep(5 * time.Millisecond)
		s := time.Now()
		v.State()
		return time.Since(s), <-done
	}
	checkNotBlocked := func(name string, op func() error) {
		wait, total := measure(op)
		limit := max(50*time.Millisecond, total/3)
		if wait > limit {
			t.Fatalf("%s 进行中 State() 等了 %v（%s 共 %v）", name, wait, name, total)
		}
	}
	checkNotBlocked("Protect", func() error { return v.Protect(password) })
	v, _ = Open(path, "dev1")
	checkNotBlocked("Unlock", func() error { return v.Unlock(password) })
	checkNotBlocked("ChangePassword", func() error { return v.ChangePassword(password, "another password") })
}
