// Package zmodem 实现 ZMODEM 文件传输协议，用于终端里的 rz（上传）和 sz（下载）。
//
// 实现依据 Chuck Forsberg 的协议文档《The ZMODEM Inter Application File Transfer Protocol》，
// 没有参考 lrzsz 等 GPL 代码。只实现终端传文件需要的部分：CRC16/CRC32、二进制和十六进制帧头、
// 流式数据包；不支持压缩、加密、断点续传和远程执行命令（ZCOMMAND 一律拒绝）。
package zmodem

import (
	"encoding/binary"
	"hash/crc32"
)

// 协议里的特殊字符。
const (
	zpad   = '*'  // 帧头前导
	zdle   = 0x18 // 转义字符（Ctrl-X），连续 5 个以上表示取消
	zbin   = 'A'  // 二进制帧头，CRC16
	zhex   = 'B'  // 十六进制帧头，CRC16
	zbin32 = 'C'  // 二进制帧头，CRC32
	xon    = 0x11
)

// 数据包结束标记（ZDLE 之后的字符）。
const (
	zcrce = 'h' // 包结束，帧结束，后面跟新的帧头
	zcrcg = 'i' // 包结束，帧继续，不需要应答
	zcrcq = 'j' // 包结束，帧继续，需要 ZACK
	zcrcw = 'k' // 包结束，帧结束，需要 ZACK
	zrub0 = 'l' // 转义后的 0x7f
	zrub1 = 'm' // 转义后的 0xff
)

// 帧类型。
const (
	zrqinit  = 0
	zrinit   = 1
	zsinit   = 2
	zack     = 3
	zfile    = 4
	zskip    = 5
	znak     = 6
	zabort   = 7
	zfin     = 8
	zrpos    = 9
	zdata    = 10
	zeof     = 11
	zferr    = 12
	zcrc     = 13
	zfreecnt = 17
	zcommand = 18
)

// ZRINIT 的能力标志，在帧头的 ZF0（第 4 个参数字节）里。
const (
	canFDX  = 0x01 // 可以全双工
	canOVIO = 0x02 // 收数据时可以同时写盘
	canFC32 = 0x20 // 支持 CRC32
	escCtl  = 0x40 // 要求发送方转义所有控制字符
)

// zcbin 是 ZFILE 的 ZF0：按二进制传输，不做换行转换。
const zcbin = 1

// cancelSequence 让对端立即中止传输：8 个 CAN 后跟 8 个退格（退格清掉对端终端上显示的乱码）。
var cancelSequence = []byte{
	zdle, zdle, zdle, zdle, zdle, zdle, zdle, zdle,
	0x08, 0x08, 0x08, 0x08, 0x08, 0x08, 0x08, 0x08,
}

// header 是一个帧头：类型和 4 个参数字节。
// 参数按协议的顺序存放：p[0] 是 ZP0（位置的最低字节），p[3] 是 ZF0（标志）。
type header struct {
	kind byte
	p    [4]byte
}

func posHeader(kind byte, pos int64) header {
	var h header
	h.kind = kind
	binary.LittleEndian.PutUint32(h.p[:], uint32(pos))
	return h
}

// pos 返回帧头里的位置参数（ZRPOS、ZDATA、ZEOF、ZACK 用）。
func (h header) pos() int64 { return int64(binary.LittleEndian.Uint32(h.p[:])) }

// flags 返回 ZF0。
func (h header) flags() byte { return h.p[3] }

// crc16 是 XMODEM 用的 CRC-16（多项式 0x1021，初值 0）。
func crc16(crc uint16, data ...byte) uint16 {
	for _, b := range data {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// ZMODEM 的 CRC32 和以太网、zip 用的相同（IEEE 多项式，初值和结果都取反），直接用 hash/crc32，
// 结果按小端顺序发送。

// encoder 生成要发给对端的字节：负责转义、帧头和数据包的编码。
type encoder struct {
	escapeCtl bool // 对端要求转义所有控制字符
	last      byte // 上一个原始字节，用于判断 '@' 后面的回车
	buf       []byte
}

// needsEscape 报告 c 是否必须转义。必须转义的有：ZDLE 本身；XON/XOFF 和 DLE（流控会吞掉它们）；
// 紧跟在 '@' 后的回车（telnet 会把 "@\r" 当成命令）；对端要求时，所有控制字符。
func (e *encoder) needsEscape(c byte) bool {
	switch c {
	case zdle, 0x10, 0x90, 0x11, 0x91, 0x13, 0x93:
		return true
	case 0x0d, 0x8d:
		if e.last&0x7f == '@' {
			return true
		}
	}
	return e.escapeCtl && (c&0x60 == 0)
}

func (e *encoder) putEscaped(data ...byte) {
	for _, c := range data {
		if e.needsEscape(c) {
			e.buf = append(e.buf, zdle, c^0x40)
		} else {
			e.buf = append(e.buf, c)
		}
		e.last = c
	}
}

// take 返回编码好的字节并清空缓冲区。
func (e *encoder) take() []byte {
	out := e.buf
	e.buf = nil
	return out
}

const hexDigits = "0123456789abcdef"

func (e *encoder) putHex(b byte) { e.buf = append(e.buf, hexDigits[b>>4], hexDigits[b&0x0f]) }

// hexHeader 编码十六进制帧头。接收方发的帧头用这种格式：全是可打印字符，穿过任何链路都不会被改坏。
func (e *encoder) hexHeader(h header) {
	e.buf = append(e.buf, zpad, zpad, zdle, zhex)
	e.putHex(h.kind)
	for _, b := range h.p {
		e.putHex(b)
	}
	crc := crc16(0, h.kind, h.p[0], h.p[1], h.p[2], h.p[3])
	e.putHex(byte(crc >> 8))
	e.putHex(byte(crc))
	// 回车、带最高位的换行；除 ZACK 和 ZFIN 外再跟一个 XON，解除对端可能处于的流控暂停。
	e.buf = append(e.buf, '\r', '\n'|0x80)
	if h.kind != zack && h.kind != zfin {
		e.buf = append(e.buf, xon)
	}
	e.last = 0
}

// binHeader 编码二进制帧头，use32 时用 CRC32。发送方的 ZFILE、ZDATA、ZEOF 用这种格式，
// 并且决定了后面数据包的 CRC 长度。
func (e *encoder) binHeader(h header, use32 bool) {
	e.buf = append(e.buf, zpad, zdle)
	if use32 {
		e.buf = append(e.buf, zbin32)
		e.putEscaped(h.kind, h.p[0], h.p[1], h.p[2], h.p[3])
		c := crc32.ChecksumIEEE([]byte{h.kind, h.p[0], h.p[1], h.p[2], h.p[3]})
		e.putEscaped(byte(c), byte(c>>8), byte(c>>16), byte(c>>24))
		return
	}
	e.buf = append(e.buf, zbin)
	e.putEscaped(h.kind, h.p[0], h.p[1], h.p[2], h.p[3])
	crc := crc16(0, h.kind, h.p[0], h.p[1], h.p[2], h.p[3])
	e.putEscaped(byte(crc>>8), byte(crc))
}

// subpacket 编码一个数据包：数据、ZDLE 加结束标记、CRC（覆盖数据和结束标记）。
func (e *encoder) subpacket(data []byte, end byte, use32 bool) {
	e.putEscaped(data...)
	e.buf = append(e.buf, zdle, end)
	if use32 {
		c := crc32.Update(0, crc32.IEEETable, data)
		c = crc32.Update(c, crc32.IEEETable, []byte{end})
		e.putEscaped(byte(c), byte(c>>8), byte(c>>16), byte(c>>24))
	} else {
		crc := crc16(crc16(0, data...), end)
		e.putEscaped(byte(crc>>8), byte(crc))
	}
	if end == zcrcw {
		e.buf = append(e.buf, xon)
	}
	e.last = 0
}
