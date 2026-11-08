// Package vault 是 Serval 的加密保险库：主机、私钥、密码都以记录的形式存在一个文件里。
//
// 设计见 docs/storage.md。要点：
//   - 记录用随机的保险库密钥（VK）以 XChaCha20-Poly1305 加密，附加数据是记录 ID 和类型；
//   - VK 由主密码经 Argon2id 派生的密钥包裹，改主密码不用重新加密记录，其他设备输入同一个主密码即可解开；
//   - 设置主密码之前保险库是“未加密”状态，只允许存放不含秘密的数据（由调用方保证）；
//   - 每条记录带 rev（时间戳加设备 ID）和删除墓碑，为以后按记录合并的同步做准备。
//
// Vault 的方法并发安全。
package vault

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// State 是保险库的状态。
type State int

const (
	// Unprotected 表示还没有设置主密码，记录以明文保存。
	Unprotected State = iota
	// Locked 表示已加密但还没有解锁，读写记录都会失败。
	Locked
	// Unlocked 表示已解锁，可以读写。
	Unlocked
)

var (
	// ErrLocked 表示保险库已锁定。
	ErrLocked = errors.New("保险库已锁定")
	// ErrWrongPassword 表示主密码不对（或者保险库文件被篡改）。
	ErrWrongPassword = errors.New("主密码不正确")
	// ErrAlreadyProtected 表示已经设置过主密码。
	ErrAlreadyProtected = errors.New("已经设置过主密码")
)

// KDF 是主密码派生参数，写在文件里，以后可以调高而不影响旧文件。
type KDF struct {
	Alg       string `json:"alg"`
	Salt      string `json:"salt"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memoryKiB"`
	Threads   uint8  `json:"threads"`
}

// DefaultKDF 是新设置主密码时使用的参数（OWASP 推荐的 Argon2id 档位之上）。
// 测试可以调低以加快速度。
var DefaultKDF = KDF{Alg: "argon2id", Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

// MinPasswordLength 是主密码的最短长度（按字符计）。
const MinPasswordLength = 8

const formatVersion = 1

// Record 是一条记录。Data 是记录内容的 JSON，只在内存里以明文存在。
type Record struct {
	ID      string
	Kind    string
	Rev     string
	Deleted bool
	Data    []byte
}

type fileRecord struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Rev     string `json:"rev"`
	Deleted bool   `json:"deleted,omitempty"`
	// Data 在加密的保险库里是 base64(nonce+密文)，未加密时是记录 JSON 的原文。
	Data string `json:"data,omitempty"`
}

// file 是保险库文件的结构。
//
// 头部字段只在提升 formatVersion 时增加：旧版本遇到更高的格式号会拒绝打开，
// 这样就不会把新版本写的头部字段在保存时丢掉。记录里的字段则可以随时增加（见 JSONChange）。
type file struct {
	Format    int    `json:"format"`
	VaultID   string `json:"vaultId"`
	Protected bool   `json:"protected"`
	KDF       *KDF   `json:"kdf,omitempty"`
	// WrappedKey 是用主密码派生的密钥加密的 VK。
	WrappedKey string `json:"wrappedVaultKey,omitempty"`
	// KeyRev 是主密码最后一次设置或修改的版本号。合并时较新的一方胜出，改主密码因此能同步到其他设备。
	KeyRev string `json:"keyRev,omitempty"`
	// PrunedBefore 是清理墓碑的水位（毫秒时间戳）：版本早于它、本地又没有的记录，
	// 合并时视为已删除，防止离线很久的设备把已删除的记录带回来。
	PrunedBefore int64        `json:"prunedBefore,omitempty"`
	Records      []fileRecord `json:"records"`
}

// Vault 是打开的保险库。
type Vault struct {
	mu     sync.Mutex
	path   string
	device string
	f      file
	key    []byte // 解锁后的 VK；未加密或锁定时为 nil
	// records 是解密后的记录。锁定时为空，原始密文留在 f.Records 里。
	records map[string]*Record
	order   []string // 记录的插入顺序，让列表稳定
	// clock 是混合逻辑时钟的当前值，见 nextRev。
	clock hlc
}

// Open 打开 path 处的保险库，文件不存在时新建一个未加密的空保险库（第一次保存时才写文件）。
// device 是本机的设备 ID，写进每条记录的 rev，用来在同步时区分不同设备的修改。
func Open(path, device string) (*Vault, error) {
	v := &Vault{path: path, device: device, records: map[string]*Record{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		v.f = file{Format: formatVersion, VaultID: newID()}
		return v, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取保险库失败: %w", err)
	}
	if err := json.Unmarshal(data, &v.f); err != nil {
		return nil, fmt.Errorf("保险库文件已损坏（%s）: %w", path, err)
	}
	if v.f.Format > formatVersion {
		return nil, fmt.Errorf("保险库是较新版本的 Serval 创建的（格式 %d），请升级 Serval", v.f.Format)
	}
	// 时钟从见过的最大版本号开始：本机时钟比上次运行时慢了，新版本号也不会倒退。
	v.clock.observe(v.f.KeyRev)
	for _, r := range v.f.Records {
		v.clock.observe(r.Rev)
	}
	if !v.f.Protected {
		for _, r := range v.f.Records {
			v.add(&Record{ID: r.ID, Kind: r.Kind, Rev: r.Rev, Deleted: r.Deleted, Data: []byte(r.Data)})
		}
	}
	return v, nil
}

func (v *Vault) add(r *Record) {
	if _, ok := v.records[r.ID]; !ok {
		v.order = append(v.order, r.ID)
	}
	v.records[r.ID] = r
}

// ID 返回保险库的唯一 ID。“在这台设备上记住”按它保存 VK。
func (v *Vault) ID() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.f.VaultID
}

// State 返回保险库当前的状态。
func (v *Vault) State() State {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.state()
}

func (v *Vault) state() State {
	switch {
	case !v.f.Protected:
		return Unprotected
	case v.key == nil:
		return Locked
	}
	return Unlocked
}

// Key 返回解锁后的保险库密钥副本，用于“在这台设备上记住”。未解锁时返回 nil。
func (v *Vault) Key() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.key)
}

// ErrKeyChanged 表示派生密钥期间主密码被修改了（例如同步合并进了另一台设备改过的主密码），需要重试。
var ErrKeyChanged = errors.New("主密码刚刚被修改，请重新输入")

// keySnapshot 是派生密钥需要的头部信息。派生（Argon2id）很慢，要在锁外进行：
// 否则派生期间任何线程调用 State() 之类的方法都会等锁，界面会卡住。
type keySnapshot struct {
	protected  bool
	vaultID    string
	kdf        KDF
	wrappedKey string
}

func (v *Vault) snapshot() keySnapshot {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := keySnapshot{protected: v.f.Protected, vaultID: v.f.VaultID, wrappedKey: v.f.WrappedKey}
	if v.f.KDF != nil {
		s.kdf = *v.f.KDF
	}
	return s
}

// openWith 在锁外用主密码解开快照里的 VK。
func (s keySnapshot) openWith(password string) ([]byte, error) {
	kek, err := deriveKey(password, &s.kdf)
	if err != nil {
		return nil, err
	}
	vk, err := open(kek, s.wrappedKey, wrapAAD(s.vaultID))
	if err != nil {
		return nil, ErrWrongPassword
	}
	return vk, nil
}

// Unlock 用主密码解锁。密钥派生在锁外进行，期间其他方法不会被阻塞。
func (v *Vault) Unlock(password string) error {
	snap := v.snapshot()
	if !snap.protected {
		return nil
	}
	vk, err := snap.openWith(password)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.f.WrappedKey != snap.wrappedKey {
		return ErrKeyChanged
	}
	return v.unlockWith(vk)
}

// UnlockWithKey 用之前保存的保险库密钥解锁。密钥不对时返回 ErrWrongPassword。
func (v *Vault) UnlockWithKey(key []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.f.Protected {
		return nil
	}
	return v.unlockWith(slices.Clone(key))
}

func (v *Vault) unlockWith(vk []byte) error {
	records := map[string]*Record{}
	var order []string
	for _, r := range v.f.Records {
		rec := &Record{ID: r.ID, Kind: r.Kind, Rev: r.Rev, Deleted: r.Deleted}
		if r.Data != "" {
			plain, err := open(vk, r.Data, recordAAD(r.ID, r.Kind))
			if err != nil {
				return ErrWrongPassword
			}
			rec.Data = plain
		}
		if _, ok := records[r.ID]; !ok {
			order = append(order, r.ID)
		}
		records[r.ID] = rec
	}
	v.key, v.records, v.order = vk, records, order
	return nil
}

// Lock 锁定保险库，丢掉内存里的密钥和明文。
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.f.Protected {
		return
	}
	clear(v.key)
	v.key, v.records, v.order = nil, map[string]*Record{}, nil
}

// Protect 为未加密的保险库设置主密码：生成新的 VK，加密全部记录并保存。
func (v *Vault) Protect(password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	snap := v.snapshot()
	if snap.protected {
		return ErrAlreadyProtected
	}
	// 派生在锁外进行，见 keySnapshot。
	kdf := DefaultKDF
	kdf.Salt = base64.StdEncoding.EncodeToString(randomBytes(16))
	kek, err := deriveKey(password, &kdf)
	if err != nil {
		return err
	}
	vk := randomBytes(chacha20poly1305.KeySize)
	wrapped := seal(kek, vk, wrapAAD(snap.vaultID))

	v.mu.Lock()
	defer v.mu.Unlock()
	if v.f.Protected {
		return ErrAlreadyProtected // 派生期间别处已经设置了主密码
	}
	next := v.f
	next.Protected, next.KDF = true, &kdf
	next.WrappedKey = wrapped
	next.KeyRev = v.nextRev("")
	previous := v.f
	v.f, v.key = next, vk
	if err := v.save(); err != nil {
		v.f, v.key = previous, nil
		return err
	}
	return nil
}

// ChangePassword 更换主密码。只重新包裹 VK，记录不用重新加密。
func (v *Vault) ChangePassword(current, next string) error {
	if err := checkPassword(next); err != nil {
		return err
	}
	snap := v.snapshot()
	if !snap.protected {
		return errors.New("还没有设置主密码")
	}
	// 两次派生都在锁外进行，见 keySnapshot。
	vk, err := snap.openWith(current)
	if err != nil {
		return err
	}
	kdf := DefaultKDF
	kdf.Salt = base64.StdEncoding.EncodeToString(randomBytes(16))
	newKEK, err := deriveKey(next, &kdf)
	if err != nil {
		return err
	}
	wrapped := seal(newKEK, vk, wrapAAD(snap.vaultID))

	v.mu.Lock()
	defer v.mu.Unlock()
	if v.f.WrappedKey != snap.wrappedKey {
		return ErrKeyChanged
	}
	previous := v.f
	v.f.KDF, v.f.WrappedKey = &kdf, wrapped
	v.f.KeyRev = v.nextRev(v.f.KeyRev)
	if err := v.save(); err != nil {
		v.f = previous
		return err
	}
	return nil
}

func checkPassword(p string) error {
	if len([]rune(p)) < MinPasswordLength {
		return fmt.Errorf("主密码至少 %d 个字符", MinPasswordLength)
	}
	return nil
}

// List 返回某一类未删除的记录，按插入顺序。
func (v *Vault) List(kind string) ([]Record, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state() == Locked {
		return nil, ErrLocked
	}
	var out []Record
	for _, id := range v.order {
		r := v.records[id]
		if r.Kind == kind && !r.Deleted {
			out = append(out, Record{ID: r.ID, Kind: r.Kind, Rev: r.Rev, Data: slices.Clone(r.Data)})
		}
	}
	return out, nil
}

// Get 返回一条未删除的记录。
func (v *Vault) Get(id string) (Record, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state() == Locked {
		return Record{}, false, ErrLocked
	}
	r, ok := v.records[id]
	if !ok || r.Deleted {
		return Record{}, false, nil
	}
	return Record{ID: r.ID, Kind: r.Kind, Rev: r.Rev, Data: slices.Clone(r.Data)}, true, nil
}

// Put 新增或替换一条记录并立即保存。
func (v *Vault) Put(kind, id string, data []byte) error {
	return v.Apply([]Change{{Kind: kind, ID: id, Data: data}})
}

// Delete 把记录标记为删除（墓碑）并立即保存。墓碑留着，以后同步时才能把删除传给其他设备。
func (v *Vault) Delete(id string) error {
	return v.Apply([]Change{{ID: id, Delete: true}})
}

// Change 是 Apply 的一项修改。
type Change struct {
	Kind, ID string
	Data     []byte
	Delete   bool
	// Known 是写入方认识的字段名。非空时 Data 合并到原记录上：只替换这些字段，
	// 原记录里其他字段（比如更新版本的客户端加的）原样保留。见 JSONChange。
	Known []string
}

// Apply 原子地应用一组修改：要么全部写入文件，要么一项都不生效。
func (v *Vault) Apply(changes []Change) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state() == Locked {
		return ErrLocked
	}
	previous := map[string]*Record{}
	order := slices.Clone(v.order)
	rollback := func() {
		for id, r := range previous {
			if r == nil {
				delete(v.records, id)
			} else {
				v.records[id] = r
			}
		}
		v.order = order
	}
	for _, ch := range changes {
		old, exists := v.records[ch.ID]
		if _, saved := previous[ch.ID]; !saved {
			previous[ch.ID] = old
		}
		if ch.Delete {
			if !exists {
				continue
			}
			v.records[ch.ID] = &Record{ID: ch.ID, Kind: old.Kind, Rev: v.nextRev(old.Rev), Deleted: true}
			continue
		}
		rev, data := "", slices.Clone(ch.Data)
		if exists {
			rev = old.Rev
			if ch.Known != nil && !old.Deleted {
				merged, err := mergeFields(old.Data, ch.Data, ch.Known)
				if err != nil {
					rollback()
					return err
				}
				data = merged
			}
		}
		v.add(&Record{ID: ch.ID, Kind: ch.Kind, Rev: v.nextRev(rev), Data: data})
	}
	if err := v.save(); err != nil {
		rollback()
		return err
	}
	return nil
}

// nextRev 生成比 previous 和本机见过的所有版本号都新的 rev（混合逻辑时钟）。调用方持有锁。
func (v *Vault) nextRev(previous string) string {
	v.clock.observe(previous)
	return v.clock.tick(time.Now().UnixMilli()).format(v.device)
}

// save 把内存里的记录写回文件，顺带清理过期的墓碑。调用方持有锁。
func (v *Vault) save() error {
	v.prune(time.Now())
	records := make([]fileRecord, 0, len(v.order))
	for _, id := range v.order {
		r := v.records[id]
		fr := fileRecord{ID: r.ID, Kind: r.Kind, Rev: r.Rev, Deleted: r.Deleted}
		if !r.Deleted {
			if v.f.Protected {
				fr.Data = seal(v.key, r.Data, recordAAD(r.ID, r.Kind))
			} else {
				fr.Data = string(r.Data)
			}
		}
		records = append(records, fr)
	}
	v.f.Records = records
	data, err := json.MarshalIndent(v.f, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(v.path, data)
}

// writeAtomic 用“写临时文件再改名”的方式保存，崩溃时不会留下写了一半的文件。
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("保存保险库失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".vault-*")
	if err != nil {
		return fmt.Errorf("保存保险库失败: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("保存保险库失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("保存保险库失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("保存保险库失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("保存保险库失败: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("保存保险库失败: %w", err)
	}
	return nil
}

func deriveKey(password string, kdf *KDF) ([]byte, error) {
	if kdf == nil || kdf.Alg != "argon2id" {
		return nil, errors.New("保险库使用了不认识的密钥派生算法")
	}
	salt, err := base64.StdEncoding.DecodeString(kdf.Salt)
	if err != nil || len(salt) < 16 || kdf.Time == 0 || kdf.MemoryKiB == 0 || kdf.Threads == 0 {
		return nil, errors.New("保险库的密钥派生参数已损坏")
	}
	return argon2.IDKey([]byte(password), salt, kdf.Time, kdf.MemoryKiB, kdf.Threads, chacha20poly1305.KeySize), nil
}

func wrapAAD(vaultID string) []byte    { return []byte("serval-vault-key:" + vaultID) }
func recordAAD(id, kind string) []byte { return []byte("serval-record:" + kind + ":" + id) }

// seal 加密 plain，返回 base64(nonce || 密文)。
func seal(key, plain, aad []byte) string {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		panic(err) // 只有密钥长度不对才会出错，属于程序错误
	}
	nonce := randomBytes(aead.NonceSize())
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, aad))
}

func open(key []byte, sealed string, aad []byte) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize() {
		return nil, errors.New("密文太短")
	}
	return aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], aad)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // 系统随机数源不可用时无法安全地继续
	}
	return b
}

// NewID 返回一个随机的记录 ID。
func NewID() string { return newID() }

func newID() string { return hex.EncodeToString(randomBytes(16)) }

// DeviceID 读取本机的设备 ID，没有就生成一个并保存在 path。设备 ID 不同步。
func DeviceID(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	}
	id := hex.EncodeToString(randomBytes(8))
	if err := writeAtomic(path, []byte(id+"\n")); err != nil {
		return "", err
	}
	return id, nil
}
