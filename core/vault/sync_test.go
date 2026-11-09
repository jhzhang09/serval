package vault

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type hostV1 struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
}

// 旧版本客户端编辑记录时，新版本加的字段原样保留；旧版本认识的字段照常更新，清空也生效。
func TestJSONChangeKeepsUnknownFields(t *testing.T) {
	v, _ := newVault(t)
	v.Put("host", "h", []byte(`{"name":"web","password":"p","totp":"新版本的字段"}`))
	ch, err := JSONChange("host", "h", hostV1{Name: "web-2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Apply([]Change{ch}); err != nil {
		t.Fatal(err)
	}
	r, _, _ := v.Get("h")
	var got map[string]string
	json.Unmarshal(r.Data, &got)
	if got["name"] != "web-2" || got["totp"] != "新版本的字段" {
		t.Fatalf("got=%v", got)
	}
	if _, ok := got["password"]; ok {
		t.Fatal("旧版本认识的字段被清空（omitempty）时应当删除，而不是保留旧值")
	}
}

// 不认识的记录类型（比如新版本加的代码片段）在保存时原样保留。
func TestUnknownKindsSurviveSave(t *testing.T) {
	v, path := newVault(t)
	v.Protect(password)
	v.Put("snippet-v9", "s", []byte(`{"x":1}`))
	v.Put("host", "h", []byte(`{}`))
	again, _ := Open(path, "dev1")
	again.Unlock(password)
	again.Put("host", "h", []byte(`{"name":"x"}`))
	third, _ := Open(path, "dev1")
	third.Unlock(password)
	if r, ok, _ := third.Get("s"); !ok || string(r.Data) != `{"x":1}` {
		t.Fatal("不认识的记录类型应保留")
	}
}

// 混合逻辑时钟：合并过对方的版本后再修改，新版本一定更大，即使本机时钟慢了很多。
func TestHLCOrdersCausally(t *testing.T) {
	a, _ := newVault(t)
	far := time.Now().Add(time.Hour).UnixMilli() // 对方时钟快一小时
	remote := hlc{ms: far, count: 3}.format("fast")
	a.mu.Lock()
	a.clock.observe(remote)
	next := a.nextRev(remote)
	a.mu.Unlock()
	if !(next > remote) {
		t.Fatalf("看过 %s 之后的修改 %s 应该更新", remote, next)
	}
	// 计数器满了借用下一毫秒，仍然递增。
	c := hlc{ms: 5, count: maxCount}
	n := c.tick(5)
	if n.ms != 6 || n.count != 0 || !(n.format("d") > hlc{ms: 5, count: maxCount}.format("d")) {
		t.Fatalf("n=%+v", n)
	}
}

// 重启后时钟从文件里见过的最大版本号继续，不会因为本机时钟回拨而倒退。
func TestClockSurvivesRestart(t *testing.T) {
	v, path := newVault(t)
	future := hlc{ms: time.Now().Add(time.Hour).UnixMilli()}.format("x")
	v.mu.Lock()
	v.records["h"] = &Record{ID: "h", Kind: "host", Rev: future, Data: []byte("{}")}
	v.order = append(v.order, "h")
	v.save()
	v.mu.Unlock()
	again, _ := Open(path, "dev1")
	again.Put("host", "other", []byte("{}"))
	r, _, _ := again.Get("other")
	if !(r.Rev > future) {
		t.Fatalf("重启后的版本号 %s 不应小于见过的 %s", r.Rev, future)
	}
}

// pair 创建同一个保险库在两台设备上的副本。
func pair(t *testing.T) (*Vault, *Vault, string, string) {
	t.Helper()
	a, pathA := newVault(t)
	a.Protect(password)
	a.Put("host", "seed", []byte(`{"name":"seed"}`))
	pathB := filepath.Join(t.TempDir(), "vault.json")
	data, _ := os.ReadFile(pathA)
	os.WriteFile(pathB, data, 0o600)
	b, err := Open(pathB, "dev2")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Unlock(password); err != nil {
		t.Fatal(err)
	}
	return a, b, pathA, pathB
}

func names(t *testing.T, v *Vault) string {
	t.Helper()
	list, err := v.List("host")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range list {
		var h hostV1
		json.Unmarshal(r.Data, &h)
		out = append(out, h.Name)
	}
	return strings.Join(out, ",")
}

func mergeInto(t *testing.T, dst, src *Vault) bool {
	t.Helper()
	data, err := src.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := dst.Merge(data)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

// 两台设备分别改了不同的记录，合并后两边的修改都在；删除也能传过去。
func TestMergeKeepsBothSides(t *testing.T) {
	a, b, _, pathB := pair(t)
	a.Put("host", "ha", []byte(`{"name":"from-a"}`))
	b.Put("host", "hb", []byte(`{"name":"from-b"}`))
	b.Delete("seed")

	if !mergeInto(t, a, b) || !mergeInto(t, b, a) {
		t.Fatal("合并应带来变化")
	}
	if names(t, a) != "from-a,from-b" || names(t, b) != "from-b,from-a" && names(t, b) != "from-a,from-b" {
		t.Fatalf("a=%s b=%s", names(t, a), names(t, b))
	}
	if mergeInto(t, a, b) {
		t.Error("再次合并相同内容不应有变化")
	}
	// 合并结果写进了文件。
	again, _ := Open(pathB, "dev2")
	again.Unlock(password)
	if !strings.Contains(names(t, again), "from-a") {
		t.Fatal("合并结果应已保存")
	}
}

// 两台设备改了同一条记录：版本号大的胜出，两边结果一致。
func TestMergeSameRecordConverges(t *testing.T) {
	a, b, _, _ := pair(t)
	a.Put("host", "seed", []byte(`{"name":"a-edit"}`))
	time.Sleep(2 * time.Millisecond)
	b.Put("host", "seed", []byte(`{"name":"b-edit"}`))
	mergeInto(t, a, b)
	mergeInto(t, b, a)
	if names(t, a) != "b-edit" || names(t, b) != "b-edit" {
		t.Fatalf("a=%s b=%s", names(t, a), names(t, b))
	}
}

// 改主密码同步到另一台设备：合并后用新主密码能解锁另一台设备的文件。
func TestMergeCarriesPasswordChange(t *testing.T) {
	a, b, _, pathB := pair(t)
	if err := a.ChangePassword(password, "brand new pw"); err != nil {
		t.Fatal(err)
	}
	mergeInto(t, b, a)
	again, _ := Open(pathB, "dev2")
	if err := again.Unlock("brand new pw"); err != nil {
		t.Fatalf("合并后应能用新主密码解锁: %v", err)
	}
}

// 墓碑过期后清理掉，并留下水位：离线很久的设备再合并时，不会把已删除的记录带回来。
func TestPrunedTombstonesDoNotResurrect(t *testing.T) {
	a, stale, _, _ := pair(t) // stale 一直保留着 seed
	a.Delete("seed")
	old := TombstoneTTL
	TombstoneTTL = time.Millisecond
	defer func() { TombstoneTTL = old }()
	time.Sleep(5 * time.Millisecond)
	a.Put("host", "new", []byte(`{"name":"new"}`)) // 保存时清理墓碑
	a.mu.Lock()
	_, stillThere := a.records["seed"]
	watermark := a.f.PrunedBefore
	a.mu.Unlock()
	if stillThere || watermark == 0 {
		t.Fatalf("过期墓碑应被清理并留下水位: stillThere=%v watermark=%d", stillThere, watermark)
	}
	mergeInto(t, a, stale)
	if names(t, a) != "new" {
		t.Fatalf("已删除的记录被带回来了: %s", names(t, a))
	}
	// 反过来，离线设备合并后也知道它该删了。
	TombstoneTTL = old
	mergeInto(t, stale, a)
	mergeInto(t, a, stale)
	if names(t, a) != "new" {
		t.Fatalf("a=%s", names(t, a))
	}
}

func TestMergeRejectsOtherVault(t *testing.T) {
	a, _ := newVault(t)
	a.Protect(password)
	other, _ := newVault(t)
	other.Protect(password)
	data, _ := other.Bytes()
	if _, err := a.Merge(data); err != ErrDifferentVault {
		t.Fatalf("err=%v", err)
	}
}

func TestAdoptReplacesEmptyVaultOnly(t *testing.T) {
	empty, emptyPath := newVault(t)
	if !empty.Empty() {
		t.Fatal("新保险库应为空")
	}
	src, _ := newVault(t)
	if err := src.Protect(password); err != nil {
		t.Fatal(err)
	}
	if err := src.Put("host", "h", []byte(`{"name":"web"}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := src.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.Adopt(raw); err != nil {
		t.Fatal(err)
	}
	if empty.Empty() || empty.State() != Locked || empty.ID() != src.ID() {
		t.Fatalf("state=%v empty=%v id=%s want %s", empty.State(), empty.Empty(), empty.ID(), src.ID())
	}
	again, err := Open(emptyPath, "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Unlock(password); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := again.Get("h"); err != nil || !ok {
		t.Fatalf("恢复后应有记录 ok=%v err=%v", ok, err)
	}
	if err := again.Put("host", "local", []byte(`{"name":"local"}`)); err != nil {
		t.Fatal(err)
	}
	other, _ := newVault(t)
	if err := other.Protect(password); err != nil {
		t.Fatal(err)
	}
	body, err := other.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Adopt(body); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("应拒绝覆盖，得到 %v", err)
	}
	if _, ok, _ := again.Get("h"); !ok {
		t.Fatal("拒绝覆盖后原记录还在")
	}
	if err := again.Adopt([]byte("{")); err == nil {
		t.Fatal("损坏文件应失败")
	}
	if _, ok, _ := again.Get("local"); !ok {
		t.Fatal("损坏文件不应改动本地")
	}

	fresh, _ := newVault(t)
	plain, _ := newVault(t)
	if err := plain.Put("host", "p", []byte(`{"name":"plain"}`)); err != nil {
		t.Fatal(err)
	}
	plainBody, err := plain.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Adopt(plainBody); err != nil {
		t.Fatal(err)
	}
	if fresh.State() != Unprotected {
		t.Fatalf("未加密文件换上后应仍未加密: %v", fresh.State())
	}
	if _, ok, _ := fresh.Get("p"); !ok {
		t.Fatal("未加密的保险库应直接可读")
	}
	if err := fresh.Adopt([]byte(`{"format":99,"vaultId":"x","records":[]}`)); err == nil {
		t.Fatal("更高的格式应拒绝")
	}
	if _, ok, _ := fresh.Get("p"); !ok {
		t.Fatal("拒绝新格式后原记录还在")
	}
}
