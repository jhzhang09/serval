// Serval 是一个轻量的原生 SSH 客户端。main 只负责组装窗口和应用状态。
package main

import (
	"log"
	"os"
	"os/user"
	"path/filepath"

	"serval/core/vault"
	"serval/design"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// configDir 返回配置目录。SERVAL_CONFIG_DIR 用于测试，避免把测试主机写入真实配置。
func configDir() string {
	if dir := os.Getenv("SERVAL_CONFIG_DIR"); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	return filepath.Join(base, "Serval")
}

// vibrancyFor 返回窗口应使用的毛玻璃材质。系统开启“减少透明度”时完全不用毛玻璃：
// 这个设置是给需要更高对比度、看半透明背景吃力的用户准备的。
// MyGo 的控件主题不会自动跟随这个设置，所以要自己处理。
func vibrancyFor(reduceTransparency bool) mygo.Vibrancy {
	if reduceTransparency {
		return mygo.VibrancyNone
	}
	return mygo.VibrancySidebar
}

func main() {
	design.RegisterFonts()

	a := newApp()
	dir := configDir()
	home, err := os.UserHomeDir()
	if err == nil {
		a.knownHosts = []string{filepath.Join(home, ".ssh", "known_hosts")}
	}
	// 全部用户数据在保险库里（见 docs/storage.md）。系统安全存储只用来“在这台设备上记住”保险库密钥。
	device, err := vault.DeviceID(filepath.Join(dir, "device-id"))
	if err != nil {
		log.Fatalf("无法创建设备 ID: %v", err)
	}
	if a.vault, err = vault.Open(filepath.Join(dir, "vault.json"), device); err != nil {
		// 打不开时不能新建一个空保险库覆盖它，宁可不启动。
		log.Fatalf("无法打开保险库: %v", err)
	}
	a.device = mygo.Secrets
	if !a.unlockWithRemembered() {
		a.reload() // 未加密时直接读出；锁定时 reload 什么都读不到，界面显示解锁页面
	}
	a.unlock.remember = true
	a.pickFile = defaultPickFile(func(dir string) ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择私钥", DefaultPath: dir, ButtonLabel: "选择", ShowHiddenFiles: true})
	})
	if u, err := user.Current(); err == nil {
		a.localUser = u.Username
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:         "Serval",
			Width:         1180,
			Height:        720,
			MinWidth:      760,
			MinHeight:     420,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHidden,
			// 侧栏和标签栏透出系统的毛玻璃；不支持的平台上看到的是窗口的底色渐变。
			Vibrancy: vibrancyFor(mygo.Theme.Preferences().ReduceTransparency),
			Content:  ui.View(a.view),
		})
		// 用户在系统设置里切换“减少透明度”时立刻跟着变，不用重启应用。
		stop := mygo.Theme.OnUpdated(func() {
			win.SetVibrancy(vibrancyFor(mygo.Theme.Preferences().ReduceTransparency))
		})
		// 后台任务的结果都通过 Update 回到主线程，再修改界面状态。
		a.post = win.Update
		win.OnClosed(func() {
			stop()
			a.closeAll()
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
