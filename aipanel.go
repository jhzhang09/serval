package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"serval/core/ai"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/ui"
)

type aiTurn struct {
	query       string
	explanation string
	commands    []string
}

// aiPanel 是 AI 助手浮层面板的状态。
type aiPanel struct {
	open         bool
	fresh        bool
	query        string
	loading      bool
	cancel       context.CancelFunc
	reqSeq       uint64
	explanation  string
	command      string
	commands     []string
	turns        []aiTurn
	chatHistory  []ai.Message
	err          string
	contextError string
	targetOS     string

	// confirmDanger 用于高危单行命令的二次确认框
	confirmDanger bool
	pendingCmd    string
}

func (a *app) activeTargetOS() string {
	t := a.current()
	if t == nil {
		return ""
	}
	if t.local {
		switch runtime.GOOS {
		case "darwin":
			return "macOS"
		case "windows":
			return "Windows"
		default:
			return "Linux"
		}
	}
	return t.host.Distro
}

// openAI 打开通用 AI 命令生成面板。
func (a *app) openAI() {
	if a.ai.cancel != nil {
		a.ai.cancel()
	}
	a.ai = aiPanel{
		open:     true,
		fresh:    true,
		targetOS: a.activeTargetOS(),
	}
}

// openAIExplain 打开 AI 错误分析面板。
func (a *app) openAIExplain(errorText string) {
	if a.ai.cancel != nil {
		a.ai.cancel()
	}
	a.ai = aiPanel{
		open:         true,
		fresh:        true,
		contextError: strings.TrimSpace(errorText),
		targetOS:     a.activeTargetOS(),
	}
	if a.ai.contextError != "" {
		a.runAIExplain()
	}
}

// openAIExplainFromActivePane 从最近复制内容或当前活动终端屏幕提取报错文本并启动分析。
func (a *app) openAIExplainFromActivePane() {
	text := strings.TrimSpace(a.lastCopiedText)
	if text == "" {
		text = a.captureActiveTerminalTail(25)
	}
	a.openAIExplain(text)
}

func (a *app) captureActiveTerminalTail(maxLines int) string {
	t := a.current()
	if t == nil || t.focus == nil || t.focus.term == nil {
		return ""
	}
	raw := t.focus.term.Text()
	lines := strings.Split(raw, "\n")
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, strings.TrimRight(line, "\r"))
		}
	}
	if len(nonEmpty) > maxLines {
		nonEmpty = nonEmpty[len(nonEmpty)-maxLines:]
	}
	return strings.Join(nonEmpty, "\n")
}

// runAIGenerate 异步调用 AI 生成或追问 Shell 命令。
func (a *app) runAIGenerate() {
	query := strings.TrimSpace(a.ai.query)
	if query == "" {
		return
	}
	if a.ai.cancel != nil {
		a.ai.cancel()
	}
	client := a.aiClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	a.ai.reqSeq++
	seq := a.ai.reqSeq
	a.ai.loading = true
	a.ai.cancel = cancel
	a.ai.err = ""

	targetOS := a.ai.targetOS
	history := append([]ai.Message(nil), a.ai.chatHistory...)
	isFollowUp := len(history) > 0

	go func() {
		var res *ai.Result
		var err error
		if isFollowUp {
			res, err = client.FollowUp(ctx, targetOS, history, query)
		} else {
			res, err = client.GenerateCommand(ctx, targetOS, query)
		}
		a.post(func() {
			canceled := ctx.Err() == context.Canceled
			cancel()
			if canceled || seq != a.ai.reqSeq {
				return
			}
			a.ai.loading = false
			a.ai.cancel = nil
			if err != nil {
				a.ai.err = friendlyAIError(err)
				return
			}
			a.applyAIResult(query, res)
			a.ai.query = ""
		})
	}()
}

// runAIExplain 异步调用 AI 分析终端报错。
func (a *app) runAIExplain() {
	errText := strings.TrimSpace(a.ai.contextError)
	if errText == "" {
		return
	}
	if a.ai.cancel != nil {
		a.ai.cancel()
	}
	client := a.aiClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	a.ai.reqSeq++
	seq := a.ai.reqSeq
	a.ai.loading = true
	a.ai.cancel = cancel
	a.ai.err = ""

	targetOS := a.ai.targetOS
	go func() {
		res, err := client.ExplainError(ctx, targetOS, errText)
		a.post(func() {
			canceled := ctx.Err() == context.Canceled
			cancel()
			if canceled || seq != a.ai.reqSeq {
				return
			}
			a.ai.loading = false
			a.ai.cancel = nil
			if err != nil {
				a.ai.err = friendlyAIError(err)
				return
			}
			a.applyAIResult("分析终端报错", res)
		})
	}()
}

func (a *app) applyAIResult(query string, res *ai.Result) {
	if res == nil {
		return
	}
	cmds := res.Commands
	if len(cmds) == 0 && strings.TrimSpace(res.Command) != "" {
		cmds = []string{strings.TrimSpace(res.Command)}
	}
	a.ai.explanation = res.Explanation
	a.ai.command = res.Command
	a.ai.commands = cmds
	a.ai.turns = append(a.ai.turns, aiTurn{
		query:       query,
		explanation: res.Explanation,
		commands:    cmds,
	})
	a.ai.chatHistory = append(a.ai.chatHistory,
		ai.Message{Role: "user", Content: query},
		ai.Message{Role: "assistant", Content: res.Raw},
	)
}

func (a *app) stopAIRequest() {
	a.ai.reqSeq++
	if a.ai.cancel != nil {
		a.ai.cancel()
		a.ai.cancel = nil
	}
	a.ai.loading = false
}

// insertCommandToTerminal 将单行命令安全填入当前激活窗格的终端，绝不自动执行（不发送回车换行）。
func (a *app) insertCommandToTerminal(cmd string) {
	cmd = strings.TrimSpace(strings.TrimRight(cmd, "\r\n"))
	if cmd == "" {
		return
	}

	// 多行脚本禁止直接填入终端（在未开启 bracketed paste 的 shell 里换行会直接触发执行）
	if strings.ContainsAny(cmd, "\r\n") {
		a.toast(ui.Toast{Title: "多行脚本不支持直接填入，请使用「复制命令」后核对执行"})
		return
	}

	// 高危单行命令二次拦截
	if ai.IsDangerousCommand(cmd) && !a.ai.confirmDanger {
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

	// 兜底剥离所有换行符，保证绝不自动敲击回车
	safeCmd := strings.NewReplacer("\r", "", "\n", "").Replace(cmd)
	t.focus.term.Paste(safeCmd)
	a.ai.open = false
	a.toast(ui.Toast{Title: "已将命令填入终端，请核对后回车执行"})
}

func (a *app) submitAIInput() {
	if a.ai.loading {
		return
	}
	if len(a.ai.turns) == 0 && a.ai.contextError != "" && strings.TrimSpace(a.ai.query) == "" {
		a.runAIExplain()
		return
	}
	a.runAIGenerate()
}

// aiDialog 渲染 AI 命令助手浮层。
func (a *app) aiDialog(c *ui.Context, colors design.Colors) {
	p := &a.ai
	if !p.open {
		if p.cancel != nil || p.loading {
			a.stopAIRequest()
		}
		return
	}
	if a.setup.open {
		return
	}
	t := c.Theme()
	_, windowHeight := c.Size()

	sheet(c, colors, &p.open, &p.fresh, 560, 22, func() {
		ui.Column(c).Gap(14).Children(func() {
			// 标题与环境提示
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, locales.AiAssistantTitle()).FontSize(design.TextHeading).FontWeight(600).Grow(1)
				envLabel := "未识别系统（按通用 Linux 处理）"
				if p.targetOS != "" {
					envLabel = "环境: " + p.targetOS
				}
				ui.Box(c).
					Padding(2, 8, 2, 8).
					Radius(10).
					Background(colors.Chip).
					Children(func() {
						ui.Text(c, envLabel).FontSize(design.TextSmall).TextColor(t.TextMuted)
					})
			})

			ui.Scroll(c).Key("aiScroll").MaxHeight(max(windowHeight-190, 180)).Children(func() {
				ui.Column(c).Gap(12).Padding(0, 4, 0, 0).Children(func() {
					// 如果有终端报错上下文
					if p.contextError != "" {
						ui.Field(c, "终端输出内容", func() {
							ui.Box(c).
								Padding(8, 10, 8, 10).
								Radius(6).
								Background(colors.Card).
								Border(1, colors.CardBorder).
								Children(func() {
									ui.Text(c, p.contextError).
										FontSize(design.TextSmall).
										Font(design.FontTerminal).
										TextColor(t.TextMuted).
										Selectable()
								})
						})
					}

					// 需求/追问输入框
					fieldLabel := "需求描述"
					placeholder := "例如：查看占用空间最大的10个文件、查看 8080 端口占用进程"
					if len(p.turns) > 0 {
						fieldLabel = "继续追问"
						placeholder = "例如：改成只看当前目录、加上按大小倒序"
					}
					ui.Field(c, fieldLabel, func() {
						c.Key("aiQueryInput")
						ui.TextInput(c, &p.query).
							Placeholder(placeholder).
							AutoFocus().
							OnSubmit(a.submitAIInput)
					})

					// 操作行
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						if p.loading {
							ui.Text(c, "AI 正在生成中…").FontSize(design.TextSmall).TextColor(t.Accent)
							ui.Button(c, locales.AiStop()).Key("aiStopBtn").OnClick(a.stopAIRequest)
						} else {
							label := locales.AiSend()
							if len(p.turns) > 0 {
								label = "发送追问"
							} else if p.contextError != "" && strings.TrimSpace(p.query) == "" {
								label = "分析报错"
							}
							ui.PrimaryButton(c, label).Key("aiSubmitBtn").OnClick(a.submitAIInput)
							if a.current() != nil {
								ui.Button(c, locales.AiAnalyzeButton()).Key("aiAnalyzeTermBtn").OnClick(func() {
									text := a.captureActiveTerminalTail(25)
									if text == "" {
										a.toast(ui.Toast{Title: "当前终端还没有可分析的输出"})
										return
									}
									p.contextError = text
									a.runAIExplain()
								})
							}
							if len(p.turns) > 0 {
								ui.Button(c, locales.AiClearChat()).Key("aiClearBtn").OnClick(func() {
									p.turns = nil
									p.chatHistory = nil
									p.explanation = ""
									p.command = ""
									p.commands = nil
									p.err = ""
								})
							}
						}
					})

					if p.err != "" {
						ui.Text(c, p.err).Key("aiError").TextColor(t.Danger).FontSize(design.TextSmall)
					}

					// 展示当前与历史对话轮次（最新结果在最上方）
					for i := len(p.turns) - 1; i >= 0; i-- {
						turn := p.turns[i]
						ui.Column(c).Gap(8).Padding(10, 12).Radius(design.RadiusRow).Background(colors.Hover).Children(func() {
							ui.Text(c, "问："+turn.query).FontSize(design.TextSmall).TextColor(t.TextMuted).Selectable()
							if turn.explanation != "" {
								ui.Text(c, turn.explanation).FontSize(design.TextBody).Selectable()
							}
							for j, cmd := range turn.commands {
								cmdVal := cmd
								isMultiline := strings.ContainsAny(cmdVal, "\r\n")
								ui.Column(c).Gap(6).Children(func() {
									ui.Box(c).
										Padding(8, 10).
										Radius(6).
										Background(colors.Card).
										Border(1, colors.CardBorder).
										Children(func() {
											ui.Text(c, cmdVal).
												FontSize(design.TextBody).
												Font(design.FontTerminal).
												Selectable()
										})
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										copyKey := fmt.Sprintf("aiCopyBtn:%d:%d", i, j)
										if i == len(p.turns)-1 && j == 0 {
											copyKey = "aiCopyBtn"
										}
										ui.Button(c, "复制命令").Key(copyKey).OnClick(func() {
											c.WriteClipboard(cmdVal)
											a.toast(ui.Toast{Title: "命令已复制到剪贴板"})
										})
										if isMultiline {
											ui.Text(c, "多行脚本请复制后核对执行").FontSize(design.TextSmall).TextColor(t.TextMuted)
										} else {
											insertKey := fmt.Sprintf("aiInsertBtn:%d:%d", i, j)
											if i == len(p.turns)-1 && j == 0 {
												insertKey = "aiInsertBtn"
											}
											ui.PrimaryButton(c, "填入终端（需确认）").Key(insertKey).OnClick(func() {
												a.insertCommandToTerminal(cmdVal)
											})
										}
									})
								})
							}
						})
					}
				})
			})

			// 底部关闭
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.Button(c, "关闭").Key("aiCloseBtn").OnClick(func() {
					a.stopAIRequest()
					p.open = false
				})
			})
		})
	})

	if !p.open && p.cancel != nil {
		a.stopAIRequest()
	}

	// 高危命令确认框（默认按钮必须是取消）
	if p.confirmDanger {
		open := true
		choice := ui.AlertDialog(c, &open, "执行高危命令确认", fmt.Sprintf("该命令包含高危操作：\n\n%s\n\n是否仍将其填入终端？（填入后仍需在终端人工敲击回车执行）", p.pendingCmd), "确认填入", locales.Cancel())
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
