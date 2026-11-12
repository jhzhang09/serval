// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// 本文件包含从 golang.org/x/crypto/ssh/internal/bcrypt_pbkdf 复用的密钥派生逻辑
// （BSD-3-Clause），用于在解析带口令的 OpenSSH 私钥时提取公钥末尾的注释。

package keys

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/pem"
	"math/big"
	"strings"

	"golang.org/x/crypto/blowfish"
	"golang.org/x/crypto/ssh"
)

const openSSHMagic = "openssh-key-v1\x00"

// openSSHComment 从 OpenSSH 私钥 PEM 中提取嵌入的注释。
// 传统 PEM 格式（如 PKCS#1/PKCS#8）不包含注释，或解析失败时返回空串，不影响导入流程。
func openSSHComment(pemBytes []byte, passphrase string) string {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "OPENSSH PRIVATE KEY" {
		return ""
	}
	if !bytes.HasPrefix(block.Bytes, []byte(openSSHMagic)) {
		return ""
	}
	var w struct {
		CipherName   string
		KdfName      string
		KdfOpts      string
		NumKeys      uint32
		PubKey       []byte
		PrivKeyBlock []byte
		Rest         []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(block.Bytes[len(openSSHMagic):], &w); err != nil || w.NumKeys != 1 {
		return ""
	}

	privBlock := w.PrivKeyBlock
	if w.CipherName != "none" {
		decrypted := decryptOpenSSHBlock(w.CipherName, w.KdfName, w.KdfOpts, privBlock, passphrase)
		if decrypted == nil {
			return ""
		}
		privBlock = decrypted
	}

	var pk1 struct {
		Check1  uint32
		Check2  uint32
		Keytype string
		Rest    []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(privBlock, &pk1); err != nil || pk1.Check1 != pk1.Check2 {
		return ""
	}

	var comment string
	switch pk1.Keytype {
	case ssh.KeyAlgoRSA:
		var k struct {
			N, E, D, Iqmp, P, Q *big.Int
			Comment             string
			Pad                 []byte `ssh:"rest"`
		}
		if err := ssh.Unmarshal(pk1.Rest, &k); err != nil {
			return ""
		}
		comment = k.Comment
	case ssh.KeyAlgoED25519:
		var k struct {
			Pub, Priv []byte
			Comment   string
			Pad       []byte `ssh:"rest"`
		}
		if err := ssh.Unmarshal(pk1.Rest, &k); err != nil {
			return ""
		}
		comment = k.Comment
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		var k struct {
			Curve   string
			Pub     []byte
			D       *big.Int
			Comment string
			Pad     []byte `ssh:"rest"`
		}
		if err := ssh.Unmarshal(pk1.Rest, &k); err != nil {
			return ""
		}
		comment = k.Comment
	default:
		return ""
	}

	return sanitizeComment(comment)
}

func sanitizeComment(s string) string {
	// authorized_keys 要求单行格式，去掉换行与控制字符避免破坏行结构。
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(cleaned)
}

func decryptOpenSSHBlock(cipherName, kdfName, kdfOpts string, encrypted []byte, passphrase string) []byte {
	if passphrase == "" || kdfName != "bcrypt" {
		return nil
	}
	var opts struct {
		Salt   string
		Rounds uint32
	}
	if err := ssh.Unmarshal([]byte(kdfOpts), &opts); err != nil {
		return nil
	}
	const maxRounds = 1 << 11
	if opts.Rounds < 1 || opts.Rounds > maxRounds || len(opts.Salt) == 0 || len(opts.Salt) > 1<<20 {
		return nil
	}
	k := bcryptPBKDF([]byte(passphrase), []byte(opts.Salt), int(opts.Rounds), 32+16)
	c, err := aes.NewCipher(k[:32])
	if err != nil {
		return nil
	}
	iv := k[32:]
	out := append([]byte(nil), encrypted...)
	switch cipherName {
	case "aes256-ctr":
		cipher.NewCTR(c, iv).XORKeyStream(out, out)
	case "aes256-cbc":
		if len(out)%c.BlockSize() != 0 {
			return nil
		}
		cipher.NewCBCDecrypter(c, iv).CryptBlocks(out, out)
	default:
		return nil
	}
	return out
}

func bcryptPBKDF(password, salt []byte, rounds, keyLen int) []byte {
	const blockSize = 32
	numBlocks := (keyLen + blockSize - 1) / blockSize
	key := make([]byte, numBlocks*blockSize)

	h := sha512.New()
	h.Write(password)
	shapass := h.Sum(nil)

	shasalt := make([]byte, 0, sha512.Size)
	cnt, tmp := make([]byte, 4), make([]byte, blockSize)
	for block := 1; block <= numBlocks; block++ {
		h.Reset()
		h.Write(salt)
		cnt[0] = byte(block >> 24)
		cnt[1] = byte(block >> 16)
		cnt[2] = byte(block >> 8)
		cnt[3] = byte(block)
		h.Write(cnt)
		bcryptHash(tmp, shapass, h.Sum(shasalt))

		out := make([]byte, blockSize)
		copy(out, tmp)
		for i := 2; i <= rounds; i++ {
			h.Reset()
			h.Write(tmp)
			bcryptHash(tmp, shapass, h.Sum(shasalt))
			for j := 0; j < len(out); j++ {
				out[j] ^= tmp[j]
			}
		}
		for i, v := range out {
			key[i*numBlocks+(block-1)] = v
		}
	}
	return key[:keyLen]
}

var bcryptMagic = []byte("OxychromaticBlowfishSwatDynamite")

func bcryptHash(out, shapass, shasalt []byte) {
	c, err := blowfish.NewSaltedCipher(shapass, shasalt)
	if err != nil {
		panic(err)
	}
	for i := 0; i < 64; i++ {
		blowfish.ExpandKey(shasalt, c)
		blowfish.ExpandKey(shapass, c)
	}
	copy(out, bcryptMagic)
	for i := 0; i < 32; i += 8 {
		for j := 0; j < 64; j++ {
			c.Encrypt(out[i:i+8], out[i:i+8])
		}
	}
	for i := 0; i < 32; i += 4 {
		out[i+3], out[i+2], out[i+1], out[i] = out[i], out[i+1], out[i+2], out[i+3]
	}
}
