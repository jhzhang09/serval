package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"serval/core/ai"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

// aiPanel 是 AI 助手浮层面板的状态。
type aiPanel struct {
	open         bool
	fresh        bool
	query        string
	loading      bool
	explanation  string
	command      string
	err          string
	contextError string
	targetOS     string

	// confirmDanger 用于高危命令的确认确认框
	confirmDanger bool
	pendingCmd    string
}

// openAI 打开通用 AI 命令生成面板。
func (a *app) openAI() {
	curOS := ""
	if t := a.current(); t != nil {
		curOS = t.host.Distro
		if curOS == "" {
			curOS = t.host.Name
		}
	}
	a.ai = aiPanel{
		open:     true,
		fresh:    true,
		targetOS: curOS,
	}
}

// openAIExplain 打开 AI 错误分析面板。
func (a *app) openAIExplain(errorText string) {
	curOS := ""
	if t := a.current(); t != nil {
		curOS = t.host.Distro
	}
	a.ai = aiPanel{
		open:         true,
		fresh:        true,
		contextError: strings.TrimSpace(errorText),
		targetOS:     curOS,
	}
	a.runAIExplain()
}

// runAIGenerate 异步调用 AI 生成 Shell 命令。
func (a *app) runAIGenerate() {
	query := strings.TrimSpace(a.ai.query)
	if query == "" {
		return
	}
	client := a.aiClient()
	a.ai.loading = true
	a.ai.err = ""
	a.ai.explanation = ""
	a.ai.command = ""

	targetOS := a.ai.targetOS
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		res, err := client.GenerateCommand(ctx, targetOS, query)
		a.post(func() {
			a.ai.loading = false
			if err != nil {
				a.ai.err = err.Error()
				return
			}
			a.ai.explanation = res.Explanation
			a.ai.command = res.Command
		})
	}()
}

// runAIExplain 异步调用 AI 分析终端报错。
func (a *app) runAIExplain() {
	errText := a.ai.contextError
	if errText == "" {
		return
	}
	client := a.aiClient()
	a.ai.loading = true
	a.ai.err = ""
	a.ai.explanation = ""
	a.ai.command = ""

	targetOS := a.ai.targetOS
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		res, err := client.ExplainError(ctx, targetOS, errText)
		a.post(func() {
			a.ai.loading = false
			if err != nil {
				a.ai.err = err.Error()
				return
			}
			a.ai.explanation = res.Explanation
			a.ai.command = res.Command
		})
	}()
}

// insertCommandToTerminal 将命令填入当前激活窗格的终端，绝不自动执行（不发送回车换行）。
func (a *app) insertCommandToTerminal(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}

	// 高危命令检查（如 rm -rf, mkfs, dd if=, 关机, 多行指令等）
	isDangerous := ai.IsDangerousCommand(cmd)
	if isDangerous && !a.ai.confirmDanger {
		a.ai.confirmDanger = true
		a.ai.pendingCmd = cmd
		return
	}

	a.ai.confirmDanger = false
	a.ai.pendingCmd = ""

	t := a.current()
	if t == nil || t.focus == nil || t.focus.term == nil {
		a.toast(ui.Toast{Title: "当前没有活动的终端窗格"})
		return
	}

	// 写入终端输入缓冲区，不带 \n，保证必须由用户人工核对后手动敲击回车执行
	t.focus.term.Paste(cmd)
	a.ai.open = false
	a.toast(ui.Toast{Title: "已将命令填入终端，请核对后回车执行"})
}

// aiDialog 渲染 AI 助手浮层。
func (a *app) aiDialog(c *ui.Context, colors design.Colors) {
	p := &a.ai
	if !p.open || a.setup.open {
		return
	}
	t := c.Theme()
	_, windowHeight := c.Size()

	sheet(c, colors, &p.open, &p.fresh, 520, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			// 标题与环境提示
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "AI 助手").FontSize(design.TextHeading).FontWeight(600).Grow(1)
				if p.targetOS != "" {
					ui.Box(c).
						Padding(2, 8, 2, 8).
						Radius(10).
						Background(colors.Chip).
						Children(func() {
							ui.Text(c, "环境: "+p.targetOS).FontSize(design.TextSmall).TextColor(t.TextMuted)
						})
				}
			})

			ui.Scroll(c).Key("aiScroll").MaxHeight(max(windowHeight-180, 160)).Children(func() {
				ui.Column(c).Gap(12).Padding(0, 4, 0, 0).Children(func() {
					// 如果有上下文报错
					if p.contextError != "" {
						ui.Field(c, "终端报错内容", func() {
							ui.Box(c).
								Padding(8, 10, 8, 10).
								Radius(6).
								Background(colors.Card).
								Children(func() {
									ui.Text(c, p.contextError).
										FontSize(design.TextSmall).
										Font(design.FontTerminal).
										TextColor(t.TextMuted)
								})
						})
					}

					// 需求输入框
					ui.Field(c, "需求描述", func() {
						c.Key("aiQueryInput")
						ui.TextInput(c, &p.query).
							Placeholder("例如：查看占用空间最大的10个文件、查看 8080 端口占用进程").
							AutoFocus()
					})

					// 生成/分析按钮
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						if p.loading {
							ui.Text(c, "AI 正在分析与生成中...").FontSize(design.TextSmall).TextColor(t.Accent)
						} else {
							label := "生成命令"
							if p.contextError != "" {
								label = "分析并给出建议"
							}
							ui.PrimaryButton(c, label).Key("aiSubmitBtn").OnClick(func() {
								if p.contextError != "" {
									a.runAIExplain()
								} else {
									a.runAIGenerate()
								}
							})
						}
					})

					if p.err != "" {
						ui.Text(c, p.err).Key("aiError").TextColor(t.Danger).FontSize(design.TextSmall)
					}

					// AI 输出展示
					if p.explanation != "" || p.command != "" {
						if p.explanation != "" {
							ui.Field(c, "原理解析", func() {
								ui.Text(c, p.explanation).FontSize(design.TextBody)
							})
						}

						if p.command != "" {
							ui.Field(c, "推荐命令", func() {
								ui.Box(c).
									Padding(10, 12, 10, 12).
									Radius(6).
									Background(colors.Card).
									Children(func() {
										ui.Text(c, p.command).
											FontSize(design.TextBody).
											Font(design.FontTerminal).
											TextColor(t.Accent)
									})
							})

							// 操作按钮组
							ui.Row(c).Gap(10).Children(func() {
								ui.Button(c, "复制命令").Key("aiCopyBtn").OnClick(func() {
									c.WriteClipboard(p.command)
									a.toast(ui.Toast{Title: "命令已复制到剪贴板"})
								})
								ui.PrimaryButton(c, "填入终端（需确认）").Key("aiInsertBtn").OnClick(func() {
									a.insertCommandToTerminal(p.command)
								})
							})
						}
					}
				})
			})

			// 底部关闭
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.Button(c, "关闭").Key("aiCloseBtn").OnClick(func() {
					p.open = false
				})
			})
		})
	})

	// 高危命令确认框
	if p.confirmDanger {
		open := true
		choice := ui.AlertDialog(c, &open, "执行高危命令确认", fmt.Sprintf("该命令包含高危操作或多行指令：\n\n%s\n\n是否仍将其填入终端？（填入后仍需在终端人工敲击回车执行）", p.pendingCmd), "确认填入", locales.Cancel())
		if choice == -1 && open {
			return
		}
		if choice == 0 {
			a.insertCommandToTerminal(p.pendingCmd)
		} else {
			p.confirmDanger = false
			p.pendingCmd = ""
		}
	}
}
