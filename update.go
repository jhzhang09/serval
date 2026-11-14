package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"serval/design"

	"github.com/egoist/mygo/ui"
)

// appVersion 是当前客户端版本号，与 mygo.json 中的 version 保持一致。
const appVersion = "0.2.0"

// defaultUpdateURL 是默认的 GitHub Releases 最新版本查询端点。
const defaultUpdateURL = "https://api.github.com/repos/jhzhang09/serval/releases/latest"

// ReleaseInfo 保存从远程 Release 获取的元数据。
type ReleaseInfo struct {
	Version     string    `json:"version"`      // 语义化版本号，如 "0.2.0"
	Title       string    `json:"title"`        // Release 标题
	Notes       string    `json:"notes"`        // 更新日志内容
	URL         string    `json:"url"`          // 网页或下载页面链接
	PublishedAt time.Time `json:"published_at"` // 发布时间
}

// githubReleaseResponse 对应 GitHub Releases latest API 的响应字段。
type githubReleaseResponse struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

type updateState int

const (
	updateIdle updateState = iota
	updateChecking
	updateAvailable
	updateLatest
	updateError
)

type updateModel struct {
	state       updateState
	checking    bool
	latest      *ReleaseInfo
	err         string
	lastChecked time.Time
	sheetOpen   bool
	sheetFresh  bool
}

// splitVersion 将版本字符串拆分为核心版本与 pre-release 标识。
// 例如 "0.2.0-beta.1" -> ("0.2.0", "beta.1")。
func splitVersion(v string) (string, string) {
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		return v[:idx], v[idx+1:]
	}
	return v, ""
}

// compareVersion 比较两个语义化版本号 v1 与 v2。
// 若 v1 > v2 返回 1；若 v1 < v2 返回 -1；若相等返回 0。
// 自动剥离前缀 "v" 或 "V" 以及首尾空格。
func compareVersion(v1, v2 string) int {
	v1 = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v1, "v"), "V"))
	v2 = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v2, "v"), "V"))

	core1, pre1 := splitVersion(v1)
	core2, pre2 := splitVersion(v2)

	parts1 := strings.Split(core1, ".")
	parts2 := strings.Split(core2, ".")
	maxLen := max(len(parts1), len(parts2))

	for i := 0; i < maxLen; i++ {
		num1, num2 := 0, 0
		if i < len(parts1) && parts1[i] != "" {
			num1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) && parts2[i] != "" {
			num2, _ = strconv.Atoi(parts2[i])
		}
		if num1 > num2 {
			return 1
		}
		if num1 < num2 {
			return -1
		}
	}

	// 核心版本完全相同，比较 pre-release：正式版优先级高于预发布版（如 0.2.0 > 0.2.0-beta）
	if pre1 == "" && pre2 != "" {
		return 1
	}
	if pre1 != "" && pre2 == "" {
		return -1
	}
	if pre1 != "" && pre2 != "" {
		if pre1 > pre2 {
			return 1
		}
		if pre1 < pre2 {
			return -1
		}
	}
	return 0
}

// isNewerVersion 判断 remote 是否比 local 版本更高。
func isNewerVersion(remote, local string) bool {
	return compareVersion(remote, local) > 0
}

// fetchLatestRelease 通过 HTTP 请求获取最新 Release 信息。
func fetchLatestRelease(ctx context.Context, client *http.Client, apiURL string) (*ReleaseInfo, error) {
	if apiURL == "" {
		return nil, errors.New("更新服务地址未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建更新检查请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "Serval-App/"+appVersion)

	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("网络连接失败，请检查网络设置: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("未找到最新版本信息")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("检查更新服务返回异常状态 (%d)", resp.StatusCode)
	}

	var ghResp githubReleaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&ghResp); err != nil {
		return nil, fmt.Errorf("解析版本信息失败: %w", err)
	}

	rawTag := strings.TrimSpace(ghResp.TagName)
	if rawTag == "" {
		return nil, errors.New("版本信息中未包含版本标签")
	}
	cleanVer := strings.TrimPrefix(strings.TrimPrefix(rawTag, "v"), "V")

	title := ghResp.Name
	if title == "" {
		title = "Serval v" + cleanVer
	}

	url := ghResp.HTMLURL
	if url == "" {
		url = "https://github.com/jhzhang09/serval/releases/tag/" + rawTag
	}

	return &ReleaseInfo{
		Version:     cleanVer,
		Title:       title,
		Notes:       ghResp.Body,
		URL:         url,
		PublishedAt: ghResp.PublishedAt,
	}, nil
}

// checkForUpdates 发起异步检查更新。
// manual 为 true 时表示由用户主动点击，无论成功与否均弹出反馈；
// manual 为 false 时表示启动等后台静默检测，仅在发现新版本时弹出，出错或已是最新时不打扰。
func (a *app) checkForUpdates(manual bool) {
	if a.update.checking {
		return
	}
	if a.updateURL == "" {
		if manual {
			a.toast(ui.Toast{Title: "检查更新失败", Description: "未配置更新检查地址", Type: "error"})
		}
		return
	}

	a.update.checking = true
	a.update.state = updateChecking
	if manual {
		a.update.err = ""
	}

	apiURL := a.updateURL
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		info, err := fetchLatestRelease(ctx, a.httpClient, apiURL)
		if a.post == nil {
			return
		}
		a.post(func() {
			a.update.checking = false
			a.update.lastChecked = time.Now()

			if err != nil {
				a.update.state = updateError
				a.update.err = err.Error()
				if manual {
					a.toast(ui.Toast{Title: "检查更新失败", Description: err.Error(), Type: "error"})
				}
				return
			}

			if isNewerVersion(info.Version, appVersion) {
				a.update.state = updateAvailable
				a.update.latest = info
				a.update.sheetOpen = true
				a.update.sheetFresh = true
				if manual {
					a.toast(ui.Toast{Title: fmt.Sprintf("发现新版本 v%s", info.Version)})
				}
			} else {
				a.update.state = updateLatest
				a.update.latest = info
				if manual {
					a.toast(ui.Toast{Title: fmt.Sprintf("当前已是最新版本 (v%s)", appVersion)})
				}
			}
		})
	}()
}

// updateDialog 渲染发现新版本时的更新卡片浮层。
func (a *app) updateDialog(c *ui.Context, colors design.Colors) {
	if !a.update.sheetOpen || a.update.latest == nil || a.setup.open {
		return
	}
	t := c.Theme()
	info := a.update.latest
	sheet(c, colors, &a.update.sheetOpen, &a.update.sheetFresh, 460, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Icon(c, design.Icon("sparkles")).FontSize(20).TextColor(t.Accent)
				verText := fmt.Sprintf("发现新版本 v%s", strings.TrimPrefix(info.Version, "v"))
				ui.Text(c, verText).FontSize(design.TextHeading).FontWeight(600).Grow(1)
				if !info.PublishedAt.IsZero() {
					ui.Text(c, info.PublishedAt.Format("2006-01-02")).FontSize(design.TextSmall).TextColor(t.TextMuted)
				}
			})

			if strings.TrimSpace(info.Notes) != "" {
				ui.Column(c).Padding(10).Radius(design.RadiusRow).Background(colors.Chip).Gap(6).Children(func() {
					ui.Text(c, "更新日志").FontSize(design.TextSmall).TextColor(t.TextMuted).FontWeight(600)
					ui.Scroll(c).Key("updateNotesScroll").MaxHeight(140).Children(func() {
						ui.Text(c, strings.TrimSpace(info.Notes)).FontSize(design.TextSmall).TextColor(t.Text).Wrap()
					})
				})
			}

			// 推荐升级途径提示
			ui.Column(c).Padding(10).Radius(design.RadiusRow).Background(colors.Hover).Gap(8).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Icon(c, design.Icon("terminal")).FontSize(14).TextColor(t.Accent)
					ui.Text(c, "macOS 推荐使用 Homebrew 升级（自动避开拦截）：").FontSize(design.TextSmall).TextColor(t.Text)
				})
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "brew upgrade serval").FontSize(design.TextSmall).Font(design.FontTerminal).
						TextColor(t.Accent).Grow(1).SingleLine().Ellipsis("…")
					ui.Button(c, "复制升级命令").Key("updateCopyBrew").Height(26).Padding(0, 10).OnClick(func() {
						c.WriteClipboard("brew upgrade serval")
						a.toast(ui.Toast{Title: "已复制 Homebrew 升级命令"})
					})
				})
			})

			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				ui.Button(c, "稍后再说").Key("updateDismiss").OnClick(func() {
					a.update.sheetOpen = false
				})
				downloadURL := info.URL
				if downloadURL == "" {
					downloadURL = "https://github.com/jhzhang09/serval/releases/latest"
				}
				ui.PrimaryButton(c, "前往 GitHub 下载").Key("updateOpenDownload").OnClick(func() {
					if a.openURL != nil {
						_ = a.openURL(downloadURL)
					}
					a.update.sheetOpen = false
				})
			})
		})
	})
	if !a.update.sheetOpen {
		a.update.sheetOpen = false
	}
}

// aboutAndUpdatesSection 在设置面板的通用设置底部渲染“关于与更新”区块。
func (a *app) aboutAndUpdatesSection(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	ui.Column(c).Gap(10).Children(func() {
		ui.Text(c, "关于与更新").FontSize(design.TextBody).FontWeight(600).TextColor(t.Text)

		ui.Field(c, "当前版本", func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("Serval v%s", appVersion)).FontSize(design.TextBody).FontWeight(600).TextColor(t.Text)

				checkBtnText := "检查更新"
				if a.update.checking {
					checkBtnText = "正在检查…"
				}
				ui.Button(c, checkBtnText).Key("settingsCheckUpdate").Disabled(a.update.checking).OnClick(func() {
					a.checkForUpdates(true)
				})

				if a.update.state == updateAvailable && a.update.latest != nil {
					ui.Button(c, fmt.Sprintf("发现新版本 v%s", strings.TrimPrefix(a.update.latest.Version, "v"))).
						Key("settingsViewUpdate").OnClick(func() {
						a.update.sheetOpen = true
						a.update.sheetFresh = true
					})
				}
			})
		}).Description("轻量、安全的原生跨平台 SSH 客户端。所有用户凭据均经端到端加密保护。")

		// 启动时自动检查更新开关
		autoCheck := !a.local.DisableAutoCheckUpdate
		c.Key("settingsAutoCheckUpdate")
		ui.Checkbox(c, &autoCheck, "启动时自动检查更新").OnChange(func() {
			next := a.local
			next.DisableAutoCheckUpdate = !autoCheck
			if err := a.saveLocal(next); err != nil {
				a.settingsPanel.err = err.Error()
			}
		})

		// 检查状态说明
		if a.update.checking {
			ui.Text(c, "正在连接 GitHub Releases 检查新版本…").FontSize(design.TextSmall).TextColor(t.TextMuted)
		} else if a.update.state == updateLatest {
			status := fmt.Sprintf("已是最新版本 (v%s)", appVersion)
			if !a.update.lastChecked.IsZero() {
				status += fmt.Sprintf(" · 上次检查于 %s", a.update.lastChecked.Format("15:04:05"))
			}
			ui.Text(c, status).FontSize(design.TextSmall).TextColor(t.Accent)
		} else if a.update.state == updateError && a.update.err != "" {
			ui.Text(c, fmt.Sprintf("检查更新失败：%s", a.update.err)).FontSize(design.TextSmall).TextColor(colors.Danger)
		}
	})
}
