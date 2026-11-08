package zmodem

import "bytes"

// Kind 是终端里出现的 ZMODEM 请求。
type Kind int

const (
	// Download：远程运行了 sz，要把文件发过来。
	Download Kind = iota + 1
	// Upload：远程运行了 rz，等着接收文件。
	Upload
)

// 远程 sz 一开始发 ZRQINIT 的十六进制帧头，远程 rz 一开始发 ZRINIT 的十六进制帧头。
var (
	sigDownload = []byte{zpad, zpad, zdle, zhex, '0', '0'}
	sigUpload   = []byte{zpad, zpad, zdle, zhex, '0', '1'}
)

// Detector 在终端输出里找 ZMODEM 的起始信号。信号可能被拆在两次读取之间，所以保留上一段的末尾。
type Detector struct {
	tail []byte
}

// Scan 检查新到的一段输出 chunk。
//
// 没找到时返回 kind 为 0，整段都应该显示到终端。找到时，chunk[:show] 是信号之前、应该显示的部分，
// session 是从信号开始的数据（可能包含上一段末尾的几个字节），交给会话作为开头。
// 信号跨两段时，上一段末尾的几个字节已经显示过了：只是 "**" 之类的字符，不影响使用。
func (d *Detector) Scan(chunk []byte) (kind Kind, show int, session []byte) {
	combined := append(append([]byte(nil), d.tail...), chunk...)
	idx, kind := -1, Kind(0)
	if i := bytes.Index(combined, sigDownload); i >= 0 {
		idx, kind = i, Download
	}
	if i := bytes.Index(combined, sigUpload); i >= 0 && (idx < 0 || i < idx) {
		idx, kind = i, Upload
	}
	if idx < 0 {
		keep := len(sigDownload) - 1
		if len(combined) < keep {
			keep = len(combined)
		}
		d.tail = append(d.tail[:0], combined[len(combined)-keep:]...)
		return 0, len(chunk), nil
	}
	d.tail = d.tail[:0]
	show = max(idx-(len(combined)-len(chunk)), 0)
	return kind, show, combined[idx:]
}

// Reset 清掉保留的末尾，会话结束后调用。
func (d *Detector) Reset() { d.tail = d.tail[:0] }
