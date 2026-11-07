package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// TombstoneTTL 是删除墓碑的保留时间。超过这个时间的墓碑在保存时清理掉；
// 离线超过这么久的设备再同步时，它在离线前就有、而这边已经删除的记录不会被带回来（见 PrunedBefore）。
var TombstoneTTL = 90 * 24 * time.Hour

// hlc 是混合逻辑时钟：物理时间（毫秒）加一个计数器。
//
// 只用物理时间时，时钟快的设备总是赢；混合逻辑时钟保证因果顺序：一台设备看过（合并过）
// 某个版本之后再做的修改，版本号一定比它大，不管两台设备的时钟差多少。
// 两台设备各自独立修改同一条记录时，仍按物理时间先后取舍，时钟偏差只影响这种真正并发的情况。
type hlc struct {
	ms    int64
	count int
}

// maxCount 是计数器的上限（4 位十进制）。同一毫秒内修改超过这么多次时借用下一毫秒。
const maxCount = 9999

// tick 返回下一个时钟值并推进时钟。
func (c *hlc) tick(now int64) hlc {
	if now > c.ms {
		c.ms, c.count = now, 0
	} else {
		c.count++
		if c.count > maxCount {
			c.ms, c.count = c.ms+1, 0
		}
	}
	return *c
}

// observe 把时钟推进到不小于 rev。
func (c *hlc) observe(rev string) {
	other, ok := parseRev(rev)
	if !ok {
		return
	}
	if other.ms > c.ms || (other.ms == c.ms && other.count > c.count) {
		*c = other
	}
}

// format 生成版本号：16 位毫秒、4 位计数器、设备 ID。定宽，所以字符串比较就是先后顺序；
// 同一时刻两台设备的版本号靠设备 ID 分出先后，合并结果在所有设备上一致。
func (c hlc) format(device string) string {
	return fmt.Sprintf("%016d-%04d-%s", c.ms, c.count, device)
}

func parseRev(rev string) (hlc, bool) {
	parts := strings.SplitN(rev, "-", 3)
	if len(parts) < 2 {
		return hlc{}, false
	}
	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return hlc{}, false
	}
	count := 0
	if len(parts) == 3 {
		if n, err := strconv.Atoi(parts[1]); err == nil {
			count = n
		}
	}
	return hlc{ms: ms, count: count}, true
}

// JSONChange 把一个结构体写成记录，同时记下这个结构体认识哪些字段。
// 写入时只替换这些字段，记录里其他字段原样保留：更新版本的客户端（比如手机）加的字段，
// 不会因为旧版本的桌面端编辑了一下就丢掉。
func JSONChange(kind, id string, value any) (Change, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Change{}, err
	}
	return Change{Kind: kind, ID: id, Data: data, Known: jsonFields(reflect.TypeOf(value))}, nil
}

// jsonFields 返回结构体序列化后的全部字段名（包括 omitempty 时可能不出现的字段）。
func jsonFields(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return []string{}
	}
	var names []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
			continue
		case name == "":
			name = f.Name
		}
		names = append(names, name)
	}
	return names
}

// mergeFields 把 next 合并到 old 上：known 里的字段以 next 为准（next 里没有就删掉，
// 对应 omitempty 清空的情况），其余字段保留 old 的值。
func mergeFields(old, next []byte, known []string) ([]byte, error) {
	var base map[string]json.RawMessage
	if err := json.Unmarshal(old, &base); err != nil || base == nil {
		return next, nil // 原记录不是对象，没有可保留的字段
	}
	var update map[string]json.RawMessage
	if err := json.Unmarshal(next, &update); err != nil {
		return nil, fmt.Errorf("记录内容不是 JSON 对象: %w", err)
	}
	for _, k := range known {
		delete(base, k)
	}
	for k, v := range update {
		base[k] = v
	}
	return json.Marshal(base)
}

// prune 清理早于 TombstoneTTL 的墓碑，并把清理水位推进到被清理墓碑的最大时间。调用方持有锁。
func (v *Vault) prune(now time.Time) {
	cutoff := now.Add(-TombstoneTTL).UnixMilli()
	kept := v.order[:0]
	for _, id := range v.order {
		r := v.records[id]
		if c, ok := parseRev(r.Rev); r.Deleted && ok && c.ms < cutoff {
			delete(v.records, id)
			v.f.PrunedBefore = max(v.f.PrunedBefore, c.ms+1)
			continue
		}
		kept = append(kept, id)
	}
	v.order = kept
}

// ErrDifferentVault 表示要合并的文件属于另一个保险库。
var ErrDifferentVault = errors.New("这是另一个保险库的文件，不能合并")

// Merge 把另一台设备的保险库文件合并进来并保存，这是同步的核心。
//
// 规则：按记录 ID 对齐，各取版本号较大的一方；删除以墓碑表示，同样参与比较；
// 本地没有、版本又早于清理水位的记录视为早已删除，不再加回来。主密码的设置按 KeyRev 取较新的一方。
// 合并只改变本地；把结果传回远端由同步方式负责（上传合并后的文件）。
// 返回本地是否发生了变化。
func (v *Vault) Merge(remote []byte) (bool, error) {
	var rf file
	if err := json.Unmarshal(remote, &rf); err != nil {
		return false, fmt.Errorf("远端的保险库文件已损坏: %w", err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	switch {
	case rf.Format > formatVersion:
		return false, fmt.Errorf("另一台设备的 Serval 版本较新（格式 %d），请先升级", rf.Format)
	case rf.VaultID != v.f.VaultID:
		return false, ErrDifferentVault
	case v.state() == Locked:
		return false, ErrLocked
	case rf.Protected && !v.f.Protected:
		return false, errors.New("另一台设备已经设置了主密码，请先在这台设备上用同一个主密码解锁")
	}

	// 先把远端记录全部解密，任何一条失败都不改动本地。
	var updates []*Record
	watermark := max(v.f.PrunedBefore, rf.PrunedBefore)
	for _, r := range rf.Records {
		v.clock.observe(r.Rev)
		local, exists := v.records[r.ID]
		if exists && local.Rev >= r.Rev {
			continue
		}
		if c, ok := parseRev(r.Rev); !exists && !r.Deleted && ok && c.ms < watermark {
			continue // 这条记录的墓碑已经被清理过，说明它早就删除了
		}
		rec := &Record{ID: r.ID, Kind: r.Kind, Rev: r.Rev, Deleted: r.Deleted}
		if !r.Deleted {
			if rf.Protected {
				plain, err := open(v.key, r.Data, recordAAD(r.ID, r.Kind))
				if err != nil {
					return false, errors.New("无法解密另一台设备的数据：两边不是同一把保险库密钥")
				}
				rec.Data = plain
			} else {
				rec.Data = []byte(r.Data)
			}
		}
		updates = append(updates, rec)
	}
	keyChanged := rf.Protected && rf.KeyRev > v.f.KeyRev
	v.clock.observe(rf.KeyRev)
	if len(updates) == 0 && !keyChanged && watermark == v.f.PrunedBefore {
		return false, nil
	}

	previousFile, previousRecords, previousOrder := v.f, cloneRecords(v.records), append([]string(nil), v.order...)
	for _, rec := range updates {
		v.add(rec)
	}
	if keyChanged {
		v.f.KDF, v.f.WrappedKey, v.f.KeyRev = rf.KDF, rf.WrappedKey, rf.KeyRev
	}
	v.f.PrunedBefore = watermark
	if err := v.save(); err != nil {
		v.f, v.records, v.order = previousFile, previousRecords, previousOrder
		return false, err
	}
	return true, nil
}

func cloneRecords(m map[string]*Record) map[string]*Record {
	out := make(map[string]*Record, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Bytes 返回当前保险库文件的内容，供同步方式上传。
func (v *Vault) Bytes() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.state() == Locked {
		return nil, ErrLocked
	}
	return json.MarshalIndent(v.f, "", "  ")
}
