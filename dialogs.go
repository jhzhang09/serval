package main

import (
	"os"
	"path/filepath"

	"serval/locales"

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
		return mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.DialogChoosePrivateKey(), DefaultPath: dir, ButtonLabel: locales.DialogChoose(), ShowHiddenFiles: true})
	})
	a.chooseUploadFile = func() (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.DialogChooseUploadFile(), DefaultPath: home, ButtonLabel: locales.Upload()})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
	a.chooseUploadFiles = func() ([]string, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.DialogChooseRzFiles(), DefaultPath: home, ButtonLabel: locales.Upload(), Multiple: true})
	}
	a.chooseFolder = func() (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: locales.DialogSaveSzTo(), DefaultPath: downloadsDir(home), ButtonLabel: locales.DialogSaveHere(), Directory: true, CreateDirectories: true})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
	a.chooseLogDir = func() (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择会话日志文件夹", DefaultPath: home, ButtonLabel: "记录到这里", Directory: true, CreateDirectories: true})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
	a.chooseUploadFolder = func() (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择要上传的文件夹", DefaultPath: home, ButtonLabel: "上传", Directory: true})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
	a.chooseDownloadFolder = func() (string, error) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "把文件夹下载到", DefaultPath: downloadsDir(home), ButtonLabel: "下载到这里", Directory: true, CreateDirectories: true})
		if err != nil || len(paths) == 0 {
			return "", err
		}
		return paths[0], nil
	}
	a.chooseSavePath = func(defaultName string) (string, error) {
		dir := downloadsDir(home)
		return mygo.Dialog.Save(mygo.SaveDialogOptions{Title: locales.DialogDownloadTo(), DefaultPath: filepath.Join(dir, defaultName), ButtonLabel: locales.Save(), CreateDirectories: true})
	}
	a.openURL = func(url string) error {
		return mygo.Shell.OpenExternal(url)
	}
}

// downloadsDir 返回“下载”文件夹，没有时用主目录。
func downloadsDir(home string) string {
	dir := filepath.Join(home, "Downloads")
	if _, err := os.Stat(dir); err != nil {
		return home
	}
	return dir
}
