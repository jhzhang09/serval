package main

import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
)

// installSystemDialogs 把系统的文件选择框接到 app 上。
//
// 这些函数字段在测试里会被替换，正式运行时只在这里设置：新增这类字段时一定要在这里接上，
// 否则正式运行时调用的是 nil 函数，程序会直接崩溃（第一批验收时“下载”就是这样崩的）。
// TestSystemDialogsAreWired 检查每个字段都设置了。
func installSystemDialogs(a *app) {
	home, _ := os.UserHomeDir()
	a.chooseKeyFile = defaultPickFile(func(dir string) ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择私钥", DefaultPath: dir, ButtonLabel: "选择", ShowHiddenFiles: true})
	})
	a.chooseUploadFile = func() (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择要上传的文件", DefaultPath: home, ButtonLabel: "上传"})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
	a.chooseSavePath = func(defaultName string) (string, error) {
		dir := filepath.Join(home, "Downloads")
		if _, err := os.Stat(dir); err != nil {
			dir = home
		}
		return mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "下载到", DefaultPath: filepath.Join(dir, defaultName), ButtonLabel: "保存", CreateDirectories: true})
	}
}
