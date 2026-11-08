package syncer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"serval/core/vault"

	"golang.org/x/net/webdav"
)

func init() {
	// 测试里用很低的派生成本，正式参数见 vault.DefaultKDF。
	vault.DefaultKDF = vault.KDF{Alg: "argon2id", Time: 1, MemoryKiB: 1024, Threads: 1}
}

const syncPassword = "correct horse"

func twoDevices(t *testing.T) (*vault.Vault, *vault.Vault) {
	t.Helper()
	dir := t.TempDir()
	a, err := vault.Open(filepath.Join(dir, "a.json"), "device-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Protect(syncPassword); err != nil {
		t.Fatal(err)
	}
	if err := a.Put("host", "seed", []byte(`{"name":"seed"}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "a.json"))
	if err != nil {
		t.Fatal(err)
	}
	pathB := filepath.Join(dir, "b.json")
	if err := os.WriteFile(pathB, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := vault.Open(pathB, "device-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Unlock(syncPassword); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func hostIDs(t *testing.T, v *vault.Vault) map[string]bool {
	t.Helper()
	list, err := v.List("host")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range list {
		out[r.ID] = true
	}
	return out
}

func mustHave(t *testing.T, v *vault.Vault, ids ...string) {
	t.Helper()
	got := hostIDs(t, v)
	for _, id := range ids {
		if !got[id] {
			t.Fatalf("缺少主机 %s，现有 %v", id, got)
		}
	}
}

func TestFileURL(t *testing.T) {
	got, err := FileURL("https://dav.example/remote/")
	if err != nil || got != "https://dav.example/remote/vault.json" {
		t.Fatalf("目录地址 = %q %v", got, err)
	}
	got, err = FileURL("http://127.0.0.1:9/vault.json")
	if err != nil || got != "http://127.0.0.1:9/vault.json" {
		t.Fatalf("文件地址 = %q %v", got, err)
	}
	if _, err := FileURL("https://user:secret@dav.example/a"); err == nil {
		t.Fatal("地址里的账号密码应被拒绝")
	}
	if _, err := FileURL("ftp://dav.example/a"); err == nil {
		t.Fatal("只接受 http 和 https")
	}
}

func TestWebDAVTwoDevicesConverge(t *testing.T) {
	a, b := twoDevices(t)
	if err := a.Put("host", "from-a", []byte(`{"name":"from-a"}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Put("host", "from-b", []byte(`{"name":"from-b"}`)); err != nil {
		t.Fatal(err)
	}
	url, client := webdavServer(t, "alice", "s3cret")
	ctx := context.Background()
	ca := &Client{URL: url, User: "alice", Password: "s3cret", HTTP: client}
	cb := &Client{URL: url, User: "alice", Password: "s3cret", HTTP: client}
	if err := Sync(ctx, a, ca); err != nil {
		t.Fatal(err)
	}
	if err := Sync(ctx, b, cb); err != nil {
		t.Fatal(err)
	}
	if err := Sync(ctx, a, ca); err != nil {
		t.Fatal(err)
	}
	mustHave(t, a, "seed", "from-a", "from-b")
	mustHave(t, b, "seed", "from-a", "from-b")

	wrong := &Client{URL: url, User: "alice", Password: "nope", HTTP: client}
	if err := Sync(ctx, a, wrong); !errors.Is(err, ErrAuth) {
		t.Fatalf("密码错误应返回 ErrAuth，得到 %v", err)
	}
}

func TestSyncRetriesWhenRemoteChanges(t *testing.T) {
	a, b := twoDevices(t)
	if err := b.Put("host", "from-b", []byte(`{"name":"from-b"}`)); err != nil {
		t.Fatal(err)
	}
	first, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Put("host", "from-b2", []byte(`{"name":"from-b2"}`)); err != nil {
		t.Fatal(err)
	}
	second, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Put("host", "from-a", []byte(`{"name":"from-a"}`)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&swapOnce{body: first, sneak: second})
	t.Cleanup(srv.Close)
	if err := Sync(context.Background(), a, &Client{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	mustHave(t, a, "seed", "from-a", "from-b", "from-b2")
	remote := openSnapshot(t, srv.Config.Handler.(*swapOnce).snapshot(), a.Key())
	mustHave(t, remote, "seed", "from-a", "from-b", "from-b2")
}

func TestSyncOfflineKeepsLocalChanges(t *testing.T) {
	a, _ := twoDevices(t)
	dir := t.TempDir()
	handler := webdavHandler(dir)
	srv := httptest.NewServer(handler)
	client := srv.Client()
	url := srv.URL + "/vault.json"
	if err := Sync(context.Background(), a, &Client{URL: url, HTTP: client}); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if err := a.Put("host", "offline", []byte(`{"name":"offline"}`)); err != nil {
		t.Fatal(err)
	}
	err := Sync(context.Background(), a, &Client{URL: url, HTTP: client})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("断网应返回 ErrUnavailable，得到 %v", err)
	}
	mustHave(t, a, "seed", "offline")

	srv = httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	if err := Sync(context.Background(), a, &Client{URL: srv.URL + "/vault.json", HTTP: srv.Client()}); err != nil {
		t.Fatal(err)
	}
	remote := openSnapshot(t, mustRead(t, filepath.Join(dir, "vault.json")), a.Key())
	mustHave(t, remote, "seed", "offline")
}

func TestSyncRejectsDifferentVault(t *testing.T) {
	a, _ := twoDevices(t)
	other, err := vault.Open(filepath.Join(t.TempDir(), "other.json"), "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Protect(syncPassword); err != nil {
		t.Fatal(err)
	}
	if err := other.Put("host", "foreign", []byte(`{"name":"foreign"}`)); err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&swapOnce{body: foreign})
	t.Cleanup(srv.Close)
	err = Sync(context.Background(), a, &Client{URL: srv.URL})
	if !errors.Is(err, vault.ErrDifferentVault) {
		t.Fatalf("另一个保险库应拒绝合并，得到 %v", err)
	}
	if !bytes.Equal(srv.Config.Handler.(*swapOnce).snapshot(), foreign) {
		t.Fatal("拒绝合并后不应覆盖远端")
	}
	if hostIDs(t, a)["foreign"] {
		t.Fatal("本机不应出现另一个保险库的主机")
	}
}

func TestUploadNeedsParentFolder(t *testing.T) {
	a, _ := twoDevices(t)
	dir := t.TempDir()
	srv := httptest.NewServer(webdavHandler(dir))
	t.Cleanup(srv.Close)
	err := Sync(context.Background(), a, &Client{URL: srv.URL + "/missing/vault.json", HTTP: srv.Client()})
	if !errors.Is(err, ErrNoFolder) {
		t.Fatalf("上一级目录不存在应返回 ErrNoFolder，得到 %v", err)
	}
	mustHave(t, a, "seed")
}

func TestConcurrentSyncConverges(t *testing.T) {
	a, b := twoDevices(t)
	if err := a.Put("host", "from-a", []byte(`{"name":"from-a"}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Put("host", "from-b", []byte(`{"name":"from-b"}`)); err != nil {
		t.Fatal(err)
	}
	url, httpClient := webdavServer(t, "alice", "s3cret")
	ctx := context.Background()
	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		errA = Sync(ctx, a, &Client{URL: url, User: "alice", Password: "s3cret", HTTP: httpClient})
	}()
	go func() {
		defer wg.Done()
		errB = Sync(ctx, b, &Client{URL: url, User: "alice", Password: "s3cret", HTTP: httpClient})
	}()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("并发同步: a=%v b=%v", errA, errB)
	}
	// 并发时后上传的那份才在服务器上。再轮流同步两轮，先同步的那台才能拿到后一台刚传上去的合并结果。
	ca := &Client{URL: url, User: "alice", Password: "s3cret", HTTP: httpClient}
	for range 2 {
		if err := Sync(ctx, a, ca); err != nil {
			t.Fatal(err)
		}
		if err := Sync(ctx, b, ca); err != nil {
			t.Fatal(err)
		}
	}
	mustHave(t, a, "seed", "from-a", "from-b")
	mustHave(t, b, "seed", "from-a", "from-b")
}

func webdavHandler(dir string) http.Handler {
	return &etagGuard{next: &webdav.Handler{
		FileSystem: webdav.Dir(dir),
		LockSystem: webdav.NewMemLS(),
	}}
}

func webdavServer(t *testing.T, user, pass string) (string, *http.Client) {
	t.Helper()
	dir := t.TempDir()
	srv := httptest.NewServer(basicAuth(user, pass, webdavHandler(dir)))
	t.Cleanup(srv.Close)
	return srv.URL + "/vault.json", srv.Client()
}

// etagGuard 让测试用的 WebDAV 遵守 If-Match 和 If-None-Match。
// golang.org/x/net/webdav 会返回 ETag，但不会检查这两个请求头。
type etagGuard struct {
	next http.Handler
	mu   sync.Mutex
}

func (e *etagGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		e.next.ServeHTTP(w, r)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	head := r.Clone(r.Context())
	head.Method = http.MethodHead
	head.Body = nil
	head.ContentLength = 0
	rec := httptest.NewRecorder()
	e.next.ServeHTTP(rec, head)
	exists := rec.Code == http.StatusOK
	etag := rec.Header().Get("ETag")
	if match := r.Header.Get("If-Match"); match != "" && (!exists || etag != match) {
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
		return
	}
	if r.Header.Get("If-None-Match") == "*" && exists {
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
		return
	}
	e.next.ServeHTTP(w, r)
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="serval"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// swapOnce 第一次带 If-Match 的上传一律拒绝，并把内容换成 sneak，用来逼出重试。
type swapOnce struct {
	mu      sync.Mutex
	body    []byte
	sneak   []byte
	etag    int
	sneaked bool
}

func (s *swapOnce) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.body...)
}

func (s *swapOnce) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if s.body == nil {
			http.NotFound(w, r)
			return
		}
		if s.etag == 0 {
			s.etag = 1
		}
		w.Header().Set("ETag", etagText(s.etag))
		if r.Method == http.MethodGet {
			w.Write(s.body)
		}
	case http.MethodPut:
		data, err := io.ReadAll(io.LimitReader(r.Body, maxVaultBytes+1))
		if err != nil || len(data) > maxVaultBytes {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		ifMatch := r.Header.Get("If-Match")
		if s.body == nil {
			if ifMatch != "" {
				http.Error(w, "precondition failed", http.StatusPreconditionFailed)
				return
			}
			s.body = data
			s.etag = 1
			w.Header().Set("ETag", etagText(s.etag))
			w.WriteHeader(http.StatusCreated)
			return
		}
		if s.etag == 0 {
			s.etag = 1
		}
		if ifMatch != etagText(s.etag) {
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
		if !s.sneaked && s.sneak != nil {
			s.sneaked = true
			s.body = append([]byte(nil), s.sneak...)
			s.etag++
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
		s.body = data
		s.etag++
		w.Header().Set("ETag", etagText(s.etag))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func etagText(n int) string {
	return `"` + itoa(n) + `"`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func openSnapshot(t *testing.T, raw, key []byte) *vault.Vault {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(path, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.UnlockWithKey(key); err != nil {
		t.Fatal(err)
	}
	return v
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
