package syncer

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"serval/core/vault"
)

type mockS3Server struct {
	mu      sync.Mutex
	objects map[string][]byte
	etags   map[string]string
}

func newMockS3Server() *mockS3Server {
	return &mockS3Server{
		objects: make(map[string][]byte),
		etags:   make(map[string]string),
	}
}

func (m *mockS3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	key := r.URL.Path
	m.mu.Lock()
	defer m.mu.Unlock()

	switch r.Method {
	case http.MethodHead:
		data, exists := m.objects[key]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", `"`+m.etags[key]+`"`)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		data, exists := m.objects[key]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", `"`+m.etags[key]+`"`)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)
		w.Write(data)

	case http.MethodPut:
		ifMatch := r.Header.Get("If-Match")
		ifNoneMatch := r.Header.Get("If-None-Match")

		_, exists := m.objects[key]
		if ifNoneMatch == "*" && exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if ifMatch != "" {
			currentETag := `"` + m.etags[key] + `"`
			if ifMatch != currentETag && ifMatch != m.etags[key] {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		h := md5.Sum(body)
		etag := hex.EncodeToString(h[:])
		m.objects[key] = body
		m.etags[key] = etag

		w.Header().Set("ETag", `"`+etag+`"`)
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestS3ParseTarget(t *testing.T) {
	cases := []struct {
		input      string
		wantBucket string
		wantKey    string
	}{
		{"s3://my-bucket", "my-bucket", "vault.json"},
		{"s3://my-bucket/custom/dir", "my-bucket", "custom/dir/vault.json"},
		{"s3://my-bucket/custom/vault.json", "my-bucket", "custom/vault.json"},
		{"my-bucket", "my-bucket", "vault.json"},
		{"my-bucket/vault.json", "my-bucket", "vault.json"},
		{"my-bucket/serval", "my-bucket", "serval/vault.json"},
	}

	for _, tc := range cases {
		b, k, err := ParseS3Target(tc.input)
		if err != nil {
			t.Fatalf("ParseS3Target(%q) 报错: %v", tc.input, err)
		}
		if b != tc.wantBucket || k != tc.wantKey {
			t.Errorf("ParseS3Target(%q) = (%q, %q), 期望 (%q, %q)", tc.input, b, k, tc.wantBucket, tc.wantKey)
		}
	}
}

func TestS3ClientLifecycle(t *testing.T) {
	mock := newMockS3Server()
	ts := httptest.NewServer(mock)
	defer ts.Close()

	client := &S3Client{
		Endpoint:        ts.URL,
		Region:          "us-east-1",
		Bucket:          "my-bucket",
		Key:             "serval/vault.json",
		AccessKeyId:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Now: func() time.Time {
			return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		},
	}

	ctx := context.Background()

	// 1. Probe (文件不存在返回 nil，表示服务联通正常)
	if err := client.Probe(ctx); err != nil {
		t.Fatalf("Probe 应当通过: %v", err)
	}

	// 2. Download 404
	_, _, err := client.Download(ctx)
	if err != ErrNotFound {
		t.Fatalf("初次 Download 应返回 ErrNotFound，实际: %v", err)
	}

	// 3. Upload (创建新文件 If-None-Match: *)
	data1 := []byte(`{"version":1,"data":"initial"}`)
	etag1, err := client.Upload(ctx, data1, "")
	if err != nil {
		t.Fatalf("初次 Upload 失败: %v", err)
	}
	if etag1 == "" {
		t.Fatal("Upload 应当返回 ETag")
	}

	// 4. Download 验证内容与 ETag
	gotData, gotETag, err := client.Download(ctx)
	if err != nil {
		t.Fatalf("Download 失败: %v", err)
	}
	if string(gotData) != string(data1) {
		t.Fatalf("数据不一致: %s != %s", gotData, data1)
	}
	if gotETag != etag1 {
		t.Fatalf("ETag 不一致: %s != %s", gotETag, etag1)
	}

	// 5. 冲突上传（错误的 ETag）
	_, err = client.Upload(ctx, []byte(`{"version":2}`), "wrong-etag")
	if err != ErrConflict {
		t.Fatalf("并发冲突时应返回 ErrConflict，实际: %v", err)
	}

	// 6. 正常覆盖（匹配的 ETag）
	data2 := []byte(`{"version":2,"data":"updated"}`)
	etag2, err := client.Upload(ctx, data2, etag1)
	if err != nil {
		t.Fatalf("带匹配 ETag 的 Upload 失败: %v", err)
	}
	if etag2 == etag1 {
		t.Fatal("更新后 ETag 应当发生改变")
	}
}

// TestS3TwoDevicesConverge 模拟两台设备通过 S3 兼容服务同步合并数据。
func TestS3TwoDevicesConverge(t *testing.T) {
	mock := newMockS3Server()
	ts := httptest.NewServer(mock)
	defer ts.Close()

	ctx := context.Background()
	newClient := func() *S3Client {
		return &S3Client{
			Endpoint:        ts.URL,
			Region:          "auto",
			Bucket:          "serval-vaults",
			Key:             "vault.json",
			AccessKeyId:     "TESTACCESSKEY",
			SecretAccessKey: "TESTSECRETKEY",
		}
	}

	// 设备 A：初始空保险库
	dirA := t.TempDir()
	vA, err := vault.Open(filepath.Join(dirA, "vault.json"), "device-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := vA.Protect("master-pass"); err != nil {
		t.Fatal(err)
	}
	if err := vA.Put("host", "host-1", []byte(`{"name":"host-A-original"}`)); err != nil {
		t.Fatal(err)
	}

	// 设备 A 同步至 S3
	if err := Sync(ctx, vA, newClient()); err != nil {
		t.Fatalf("设备 A 同步失败: %v", err)
	}

	// 设备 B：从 S3 下载初始内容并 Setup
	dirB := t.TempDir()
	vB, err := vault.Open(filepath.Join(dirB, "vault.json"), "device-b")
	if err != nil {
		t.Fatal(err)
	}
	s3B := newClient()
	body, _, err := s3B.Download(ctx)
	if err != nil {
		t.Fatalf("设备 B 下载失败: %v", err)
	}
	if err := vB.Adopt(body); err != nil {
		t.Fatalf("设备 B Adopt 失败: %v", err)
	}
	if err := vB.Unlock("master-pass"); err != nil {
		t.Fatalf("设备 B 解锁失败: %v", err)
	}

	// 两台设备并发写入不同记录
	if err := vA.Put("host", "host-2", []byte(`{"name":"host-A-new"}`)); err != nil {
		t.Fatal(err)
	}
	if err := vB.Put("host", "host-3", []byte(`{"name":"host-B-new"}`)); err != nil {
		t.Fatal(err)
	}

	// 设备 B 先同步
	if err := Sync(ctx, vB, newClient()); err != nil {
		t.Fatalf("设备 B 上传合并失败: %v", err)
	}

	// 设备 A 后同步（将自动经历下载远端 -> 合并 -> 上传合并结果）
	if err := Sync(ctx, vA, newClient()); err != nil {
		t.Fatalf("设备 A 冲突后自动合并同步失败: %v", err)
	}

	// 设备 B 再次同步，两端达到完全收敛
	if err := Sync(ctx, vB, newClient()); err != nil {
		t.Fatalf("设备 B 最终同步失败: %v", err)
	}

	// 验证 A 和 B 均拥有全部 3 条记录
	for _, id := range []string{"host-1", "host-2", "host-3"} {
		recA, existsA, errA := vA.Get(id)
		if errA != nil || !existsA || len(recA.Data) == 0 {
			t.Errorf("设备 A 缺少记录 %s: exists=%v, err=%v", id, existsA, errA)
		}
		recB, existsB, errB := vB.Get(id)
		if errB != nil || !existsB || len(recB.Data) == 0 {
			t.Errorf("设备 B 缺少记录 %s: exists=%v, err=%v", id, existsB, errB)
		}
	}
}

func TestS3TargetFormatAndParse(t *testing.T) {
	target := S3Target{
		Endpoint: "https://custom.endpoint.com:9000",
		Region:   "auto",
		Bucket:   "backup-bucket",
		Key:      "serval/vault.json",
	}

	formatted := target.FormatURL()
	parsed, err := ParseS3URL(formatted)
	if err != nil {
		t.Fatalf("ParseS3URL 失败: %v", err)
	}

	if parsed.Endpoint != target.Endpoint {
		t.Errorf("Endpoint: %s != %s", parsed.Endpoint, target.Endpoint)
	}
	if parsed.Region != target.Region {
		t.Errorf("Region: %s != %s", parsed.Region, target.Region)
	}
	if parsed.Bucket != target.Bucket {
		t.Errorf("Bucket: %s != %s", parsed.Bucket, target.Bucket)
	}
	if parsed.Key != target.Key {
		t.Errorf("Key: %s != %s", parsed.Key, target.Key)
	}

	client, err := NewS3ClientFromURL(formatted, "test-ak", "test-sk")
	if err != nil {
		t.Fatalf("NewS3ClientFromURL 失败: %v", err)
	}
	if client.Endpoint != target.Endpoint || client.AccessKeyId != "test-ak" || client.SecretAccessKey != "test-sk" {
		t.Errorf("构造的 client 字段不符合预期: %+v", client)
	}
}
