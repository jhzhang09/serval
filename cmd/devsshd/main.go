// devsshd 在本机回环地址上启动开发用 SSH 服务器，用于手动验证和性能测量。
//
//	SERVAL_DEV_PASSWORD=<临时密码> go run ./cmd/devsshd -addr 127.0.0.1:2222
//	go run ./cmd/devsshd -authorized-key ~/.ssh/test_key.pub   # 只允许这把钥匙登录
//
// 服务器以当前用户身份运行真实 shell，请只用临时密码，用完立即关闭。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"serval/internal/devsshd"

	"golang.org/x/crypto/ssh"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2222", "监听地址，必须是回环地址")
	shell := flag.String("shell", "/bin/zsh", "启动的 shell")
	keyFile := flag.String("authorized-key", "", "允许登录的公钥文件（.pub），用于验证私钥登录")
	flag.Parse()

	opts := devsshd.Options{Addr: *addr, Password: os.Getenv("SERVAL_DEV_PASSWORD"), Shell: []string{*shell}}
	if *keyFile != "" {
		data, err := os.ReadFile(*keyFile)
		if err != nil {
			log.Fatal(err)
		}
		if opts.AuthorizedKey, _, _, _, err = ssh.ParseAuthorizedKey(data); err != nil {
			log.Fatalf("解析公钥失败: %v", err)
		}
	}
	if opts.Password == "" && opts.AuthorizedKey == nil {
		log.Fatal("请通过 SERVAL_DEV_PASSWORD 提供临时密码，或用 -authorized-key 指定公钥")
	}
	server, err := devsshd.Start(opts)
	if err != nil {
		log.Fatal(err)
	}
	defer server.Close()
	fmt.Printf("devsshd 正在监听 %s\n", server.Addr())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
