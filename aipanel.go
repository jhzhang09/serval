package main

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"serval/core/ai"
	"serval/core/vault"
	"serval/design"
	"serval/locales"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/plugins/loading"
	"github.com/egoist/mygo/ui"
)

type aiToolStatus uint8

const (
	toolRunning aiToolStatus = iota
	toolWaitingApproval
	toolDone
	toolError
	toolDenied
)

type aiToolRecord struct {
	ID        string
	Name      string
	Summary   string
	Args      string
	Output    string
	Status    aiToolStatus
	Expanded  bool
	StartedAt time.Time
}

type aiStepRecord struct {
	Step            int
	Thinking        string
	ThinkingSeconds int
	ThinkExpanded   bool
	Content         string
	Tools           []*aiToolRecord
}

type aiTurn struct {
	query           string
	prompt          string
	attachedIDs     []string
	extraErr        string
	contextTags     []string
	steps           []*aiStepRecord
	usage           ai.Usage
	running         bool
	startedAt       time.Time
	modelName       string
	err             string
	historyStart    int
	reachedMax      bool
	compactedNotice string

	// 兼容字段：从最终回答同步提取的主解释与可执行命令块
	explanation string
	commands    []string
}

type aiApprovalKind uint8

const (
	approvalExec aiApprovalKind = iota
	approvalFileWrite
	approvalInsert
)

type aiApprovalDecision struct {
	approved     bool
	editedCmd    string
	allowSession bool
}

type aiApproval struct {
	seq        uint64
	tool       *aiToolRecord
	kind       aiApprovalKind
	command    string
	reason     string
	target     string
	diff       string
	filePath   string
	baseProg   string
	editing    bool
	dangerous  bool
	production bool
	answer     chan aiApprovalDecision
	scroll     ui.ScrollState
}

type aiPlanStatus string

const (
	planPending    aiPlanStatus = "pending"
	planInProgress aiPlanStatus = "in_progress"
	planCompleted  aiPlanStatus = "completed"
	planFailed     aiPlanStatus = "failed"
)

type aiPlanStep struct {
	Title  string       `json:"title"`
	Status aiPlanStatus `json:"status"`
	Note   string       `json:"note,omitempty"`
}

// aiConversation 表示某个终端标签页内的一条独立历史/当前 AI 对话会话。
type aiConversation struct {
	ID                  string
	Title               string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Turns               []aiTurn
	ChatHistory         []ai.Message
	Plan                []aiPlanStep
	PlanExpanded        bool
	Explanation         string
	Command             string
	Commands            []string
	Loading             bool
	Cancel              context.CancelFunc
	ReqSeq              uint64
	Err                 string
	ContextError        string
	Attached            []string
	PendingApproval     *aiApproval
	OutputStore         *ai.ToolOutputStore
	SessionAllowedProgs map[string]bool
	ScrollY             float32
}

type aiSavedTool struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
	Args    string `json:"args,omitempty"`
	Output  string `json:"output,omitempty"`
	Status  uint8  `json:"status,omitempty"`
}

type aiSavedStep struct {
	Step            int           `json:"step"`
	Thinking        string        `json:"thinking,omitempty"`
	ThinkingSeconds int           `json:"thinkingSeconds,omitempty"`
	Content         string        `json:"content,omitempty"`
	Tools           []aiSavedTool `json:"tools,omitempty"`
}

type aiSavedTurn struct {
	Query       string        `json:"query,omitempty"`
	Prompt      string        `json:"prompt,omitempty"`
	AttachedIDs []string      `json:"attachedIds,omitempty"`
	ExtraErr    string        `json:"extraErr,omitempty"`
	ContextTags []string      `json:"contextTags,omitempty"`
	Steps       []aiSavedStep `json:"steps,omitempty"`
	Usage       ai.Usage      `json:"usage,omitempty"`
	Err         string        `json:"err,omitempty"`
	Explanation string        `json:"explanation,omitempty"`
	Commands    []string      `json:"commands,omitempty"`
}

// aiChatRecord 是持久化到加密保险库中的 AI 历史会话记录。
type aiChatRecord struct {
	ID          string        `json:"id"`
	Scope       string        `json:"scope"`
	Title       string        `json:"title"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
	Turns       []aiSavedTurn `json:"turns,omitempty"`
	ChatHistory []ai.Message  `json:"chatHistory,omitempty"`
	Plan        []aiPlanStep  `json:"plan,omitempty"`
	Explanation string        `json:"explanation,omitempty"`
	Command     string        `json:"command,omitempty"`
	Commands    []string      `json:"commands,omitempty"`
}

// aiPanel 是右侧停靠的 AI Agent 侧栏状态（每个标签页各自拥有独立的一份）。
type aiPanel struct {
	open         bool
	fresh        bool
	query        string
	loading      bool
	cancel       context.CancelFunc
	reqSeq       uint64
	mode         aiPermissionMode
	explanation  string
	command      string
	commands     []string
	turns        []aiTurn
	chatHistory  []ai.Message
	err          string
	contextError string
	targetOS     string

	// 会话管理：每个终端可保存多条历史对话并自由新开或切换
	convID      string
	convTitle   string
	convCreated time.Time
	convs       []*aiConversation
	showHistory bool
	convSeq     int

	// @ 上下文引用与 / 快捷命令浮层状态
	attachedContexts []string // "terminal", "host", "sftp", "clipboard"
	showMentions     bool
	showSlash        bool
	popupIdx         int

	// 历史会话行内重命名状态
	renamingConvID  string
	renameConvDraft string

	// 等待人工审批的工具执行请求
	pendingApproval *aiApproval

	// 结构化执行计划（Plan / Todo）
	plan         []aiPlanStep
	planExpanded bool

	// 滚动状态
	scroll     ui.ScrollState
	autoScroll bool

	// confirmDanger 用于代码块点击“填入终端”触发高危命令时的二次确认框
	confirmDanger bool
	pendingCmd    string
}

func (s *aiPanel) convByID(id string) *aiConversation {
	if s == nil || id == "" {
		return nil
	}
	for _, c := range s.convs {
		if c != nil && c.ID == id {
			return c
		}
	}
	return nil
}

type aiSlashCommand struct {
	Cmd     string
	Title   string
	Desc    string
	Prompt  string
	Attach  []string
	IsClear bool
	IsNew   bool
}

var aiSlashCommands = []aiSlashCommand{
	{
		Cmd:    "/inspect",
		Title:  "服务器配置体检",
		Desc:   "全面检查系统、CPU、内存、磁盘、网络与监听端口",
		Prompt: "这台服务器配置和当前运行状态如何？请主动检查系统内核、CPU、内存、磁盘、网络接口、关键进程与监听端口并汇总成表格。",
		Attach: []string{"host"},
	},
	{
		Cmd:    "/diagnose",
		Title:  "终端报错诊断",
		Desc:   "结合终端最近输出分析报错根因与排查修复方案",
		Prompt: "请读取当前终端最近的输出和报错信息，分析根因并给出排查与修复建议。",
		Attach: []string{"terminal", "host"},
	},
	{
		Cmd:    "/ports",
		Title:  "端口与进程排查",
		Desc:   "查看监听端口、对应服务进程与网络连接状态",
		Prompt: "请检查当前服务器正在监听的端口和主要进程占用情况，列出端口、协议与对应服务。",
		Attach: []string{"host"},
	},
	{
		Cmd:    "/logs",
		Title:  "排查近期异常日志",
		Desc:   "检查系统日志与常见服务异常记录",
		Prompt: "请检查这台主机最近的系统与服务错误日志，定位是否有明显异常或告警。",
		Attach: []string{"host"},
	},
	{
		Cmd:   "/new",
		Title: "新开会话",
		Desc:  "保存当前对话到历史并开启新会话",
		IsNew: true,
	},
	{
		Cmd:     "/clear",
		Title:   "清空当前对话",
		Desc:    "重置当前 AI Agent 会话历史与上下文",
		IsClear: true,
	},
}

type aiMentionOption struct {
	ID    string
	Label string
	Desc  string
}

var aiMentionOptions = []aiMentionOption{
	{ID: "terminal", Label: "终端输出", Desc: "附加当前活动终端最近 80 行内容"},
	{ID: "host", Label: "主机信息", Desc: "附加当前主机名、系统、登录用户与工作目录"},
	{ID: "sftp", Label: "SFTP目录", Desc: "附加当前 SFTP 浏览目录与远端路径"},
	{ID: "clipboard", Label: "最近复制", Desc: "附加最近一次复制的终端文本"},
}

func (a *app) activeTargetOS() string {
	t := a.current()
	if t == nil {
		return ""
	}
	if t.local {
		return localOSName()
	}
	return t.host.Distro
}

func (a *app) currentAITabID() int {
	if t := a.current(); t != nil && t.id > 0 {
		return t.id
	}
	return 0
}

// syncAITab 在切换标签页后把当前 a.ai 状态保存回旧标签，并切出新标签独立的 AI 会话与抽屉开关状态。
func (a *app) syncAITab() {
	curID := a.currentAITabID()
	if curID == a.aiActiveTabID {
		return
	}
	if a.aiSessions == nil {
		a.aiSessions = map[int]*aiPanel{}
	}
	saveActiveConversation(&a.ai)

	// 若从空状态（0）打开了第一个真实标签页，且空状态里已有对话或已展开面板，则迁移到新标签页避免会话丢失。
	if a.aiActiveTabID == 0 && curID > 0 {
		hasContent := a.ai.open || len(a.ai.turns) > 0 || a.ai.loading || strings.TrimSpace(a.ai.query) != ""
		next, exists := a.aiSessions[curID]
		nextEmpty := !exists || next == nil || (!next.open && len(next.turns) == 0 && !next.loading)
		if hasContent && nextEmpty {
			a.ai.targetOS = a.activeTargetOS()
			if a.aiTabRedirects == nil {
				a.aiTabRedirects = map[int]int{}
			}
			a.aiTabRedirects[0] = curID
			delete(a.aiSessions, 0)
			a.aiActiveTabID = curID
			a.mergePersistedConvsInto(&a.ai, a.aiScopeKeyForTabID(curID))
			if c := a.ai.convByID(a.ai.convID); c != nil && len(c.Turns) > 0 {
				a.persistAIConversation(curID, c)
			}
			return
		}
	}

	prev := a.ai
	a.aiSessions[a.aiActiveTabID] = &prev

	mode := a.ai.mode
	if next, ok := a.aiSessions[curID]; ok && next != nil {
		a.ai = *next
	} else {
		a.ai = aiPanel{
			mode:     mode,
			targetOS: a.activeTargetOS(),
		}
		a.mergePersistedConvsInto(&a.ai, a.aiScopeKeyForTabID(curID))
	}
	a.aiActiveTabID = curID
}

// aiSessionForTab 返回指定标签页对应的 AI 会话状态指针，后台协程更新仅作用于发起该轮对话的标签页。
func (a *app) aiSessionForTab(tabID int) *aiPanel {
	a.syncAITab()
	if tabID == 0 {
		if redir, ok := a.aiTabRedirects[0]; ok && redir > 0 {
			tabID = redir
		}
	}
	if tabID == a.aiActiveTabID {
		return &a.ai
	}
	if a.aiSessions == nil {
		a.aiSessions = map[int]*aiPanel{}
	}
	if s, ok := a.aiSessions[tabID]; ok && s != nil {
		return s
	}
	s := &aiPanel{}
	a.aiSessions[tabID] = s
	return s
}

func stopAIConversation(c *aiConversation) {
	if c == nil {
		return
	}
	c.ReqSeq++
	if c.Cancel != nil {
		c.Cancel()
		c.Cancel = nil
	}
	if c.PendingApproval != nil {
		if c.PendingApproval.tool != nil && c.PendingApproval.tool.Status == toolWaitingApproval {
			c.PendingApproval.tool.Status = toolDenied
		}
		select {
		case c.PendingApproval.answer <- aiApprovalDecision{}:
		default:
		}
		c.PendingApproval = nil
	}
	c.Loading = false
	for i := range c.Turns {
		c.Turns[i].running = false
	}
}

func stopActiveAIConversation(s *aiPanel) {
	if s == nil {
		return
	}
	s.reqSeq++
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.pendingApproval != nil {
		if s.pendingApproval.tool != nil && s.pendingApproval.tool.Status == toolWaitingApproval {
			s.pendingApproval.tool.Status = toolDenied
		}
		select {
		case s.pendingApproval.answer <- aiApprovalDecision{}:
		default:
		}
		s.pendingApproval = nil
	}
	s.loading = false
	for i := range s.turns {
		s.turns[i].running = false
	}
}

func stopAISession(s *aiPanel) {
	if s == nil {
		return
	}
	stopActiveAIConversation(s)
	for _, c := range s.convs {
		stopAIConversation(c)
	}
}

func (a *app) closeAISessionForTab(tabID int) {
	if tabID <= 0 {
		return
	}
	if tabID == a.aiActiveTabID {
		stopAISession(&a.ai)
		mode := a.ai.mode
		a.ai = aiPanel{mode: mode}
		a.aiActiveTabID = 0
	}
	if s, ok := a.aiSessions[tabID]; ok {
		stopAISession(s)
		delete(a.aiSessions, tabID)
	}
}

func (a *app) stopAllAISessions() {
	stopAISession(&a.ai)
	for id, s := range a.aiSessions {
		stopAISession(s)
		delete(a.aiSessions, id)
	}
	a.aiSessions = nil
	a.aiActiveTabID = 0
	a.ai = aiPanel{}
}

func convTitleFromTurns(turns []aiTurn) string {
	for _, t := range turns {
		q := strings.TrimSpace(strings.ReplaceAll(t.query, "\n", " "))
		if q == "" {
			continue
		}
		r := []rune(q)
		if len(r) > 36 {
			return string(r[:36]) + "…"
		}
		return q
	}
	return "新会话"
}

func saveActiveConversation(s *aiPanel) {
	if s == nil {
		return
	}
	if len(s.turns) == 0 && !s.loading && s.convID == "" {
		return
	}
	now := time.Now()
	if s.convID == "" {
		s.convSeq++
		s.convID = vault.NewID()
		s.convCreated = now
	}
	if s.convTitle == "" && len(s.turns) > 0 {
		s.convTitle = convTitleFromTurns(s.turns)
	}
	title := s.convTitle
	if title == "" {
		title = "新会话"
	}
	created := s.convCreated
	if created.IsZero() {
		created = now
	}
	c := s.convByID(s.convID)
	if c == nil {
		c = &aiConversation{ID: s.convID, CreatedAt: created}
		s.convs = append([]*aiConversation{c}, s.convs...)
	}
	c.Title = title
	c.UpdatedAt = now
	c.Turns = s.turns
	c.ChatHistory = s.chatHistory
	c.Plan = s.plan
	c.PlanExpanded = s.planExpanded
	c.Explanation = s.explanation
	c.Command = s.command
	c.Commands = s.commands
	c.Loading = s.loading
	c.Cancel = s.cancel
	c.ReqSeq = s.reqSeq
	c.Err = s.err
	c.ContextError = s.contextError
	c.Attached = s.attachedContexts
	c.PendingApproval = s.pendingApproval
	c.ScrollY = s.scroll.Y
}

func loadConversationInto(s *aiPanel, c *aiConversation) {
	if s == nil || c == nil {
		return
	}
	s.convID = c.ID
	s.convTitle = c.Title
	s.convCreated = c.CreatedAt
	s.turns = c.Turns
	s.chatHistory = c.ChatHistory
	s.plan = c.Plan
	s.planExpanded = c.PlanExpanded
	s.explanation = c.Explanation
	s.command = c.Command
	s.commands = c.Commands
	s.loading = c.Loading
	s.cancel = c.Cancel
	s.reqSeq = c.ReqSeq
	s.err = c.Err
	s.contextError = c.ContextError
	s.attachedContexts = c.Attached
	s.pendingApproval = c.PendingApproval
	s.scroll.Y = c.ScrollY
	s.showHistory = false
	s.showMentions = false
	s.showSlash = false
	s.confirmDanger = false
	s.pendingCmd = ""
}

func resetActiveConversationDraft(s *aiPanel, targetOS string) {
	if s == nil {
		return
	}
	open, mode, convs, seq := s.open, s.mode, s.convs, s.convSeq
	*s = aiPanel{
		open:     open,
		mode:     mode,
		targetOS: targetOS,
		convs:    convs,
		convSeq:  seq,
	}
}

// newAIConversation 保存当前会话到本终端历史记录并开启一个新的空白会话。
func (a *app) newAIConversation() {
	a.syncAITab()
	saveActiveConversation(&a.ai)
	resetActiveConversationDraft(&a.ai, a.activeTargetOS())
}

// selectAIConversation 在当前终端内切换到指定历史会话。
func (a *app) selectAIConversation(id string) {
	a.syncAITab()
	if id == a.ai.convID {
		a.ai.showHistory = false
		return
	}
	saveActiveConversation(&a.ai)
	c := a.ai.convByID(id)
	if c == nil {
		return
	}
	loadConversationInto(&a.ai, c)
}

// deleteAIConversation 删除当前终端内的一条历史会话（若为当前会话则切换回新空白页）。
func (a *app) deleteAIConversation(id string) {
	a.syncAITab()
	idx := slices.IndexFunc(a.ai.convs, func(c *aiConversation) bool { return c != nil && c.ID == id })
	if idx >= 0 {
		stopAIConversation(a.ai.convs[idx])
		a.ai.convs = slices.Delete(a.ai.convs, idx, idx+1)
	}
	if a.ai.convID == id {
		showHist := a.ai.showHistory
		stopActiveAIConversation(&a.ai)
		resetActiveConversationDraft(&a.ai, a.activeTargetOS())
		a.ai.showHistory = showHist
	}
	a.deletePersistedAIConversation(id)
}

// openAI 打开 AI Agent 侧栏并定位到当前活动环境（保留该标签页自己的对话记录与权限模式）。
func (a *app) openAI() {
	a.syncTabDrawers()
	a.syncAITab()
	a.mergePersistedConvsInto(&a.ai, a.aiScopeKeyForTabID(a.aiActiveTabID))
	a.ai.open = true
	a.ai.fresh = true
	a.lastDrawer = "ai"
	if cur := a.current(); cur != nil {
		cur.lastDrawer = "ai"
	}
	a.ai.targetOS = a.activeTargetOS()
}

func aiScopeKeyForTab(t *tab) string {
	if t == nil {
		return "global"
	}
	if t.local {
		return "local"
	}
	if id := strings.TrimSpace(t.host.ID); id != "" {
		return "host:" + id
	}
	port := t.host.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("addr:%s@%s:%d", t.host.User, t.host.Address, port)
}

func (a *app) aiScopeKeyForTabID(tabID int) string {
	if tabID <= 0 {
		return "global"
	}
	return aiScopeKeyForTab(a.tabByID(tabID))
}

func sortAIConversations(convs []*aiConversation) {
	slices.SortStableFunc(convs, func(x, y *aiConversation) int {
		if x == nil && y == nil {
			return 0
		}
		if x == nil {
			return 1
		}
		if y == nil {
			return -1
		}
		if c := y.UpdatedAt.Compare(x.UpdatedAt); c != 0 {
			return c
		}
		if c := y.CreatedAt.Compare(x.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(x.ID, y.ID)
	})
}

func conversationToRecord(scope string, c *aiConversation) aiChatRecord {
	turns := make([]aiSavedTurn, 0, len(c.Turns))
	for _, t := range c.Turns {
		steps := make([]aiSavedStep, 0, len(t.steps))
		for _, s := range t.steps {
			if s == nil {
				continue
			}
			th, thTrunc := sanitizeToolText(s.Thinking, 2048)
			if thTrunc {
				th += "\n...[思考过程已截断]"
			}
			tools := make([]aiSavedTool, 0, len(s.Tools))
			for _, tl := range s.Tools {
				if tl == nil {
					continue
				}
				st := tl.Status
				if st == toolRunning {
					st = toolError
				} else if st == toolWaitingApproval {
					st = toolDenied
				}
				out, outTrunc := sanitizeToolText(tl.Output, 3072)
				if outTrunc {
					out += "\n...[历史保存已截断]"
				}
				tools = append(tools, aiSavedTool{
					ID:      tl.ID,
					Name:    tl.Name,
					Summary: tl.Summary,
					Args:    tl.Args,
					Output:  out,
					Status:  uint8(st),
				})
			}
			steps = append(steps, aiSavedStep{
				Step:            s.Step,
				Thinking:        th,
				ThinkingSeconds: s.ThinkingSeconds,
				Content:         s.Content,
				Tools:           tools,
			})
		}
		turns = append(turns, aiSavedTurn{
			Query:       t.query,
			Prompt:      t.prompt,
			AttachedIDs: slices.Clone(t.attachedIDs),
			ExtraErr:    t.extraErr,
			ContextTags: slices.Clone(t.contextTags),
			Steps:       steps,
			Usage:       t.usage,
			Err:         t.err,
			Explanation: t.explanation,
			Commands:    slices.Clone(t.commands),
		})
	}
	hist := ai.SanitizeMessageSequence(compressChatHistory(c.ChatHistory))
	if len(hist) > 24 {
		hist = ai.SanitizeMessageSequence(hist[len(hist)-24:])
	}
	cleanHist := make([]ai.Message, len(hist))
	for i, m := range hist {
		m.ReasoningContent = ""
		m.ReasoningDetails = nil
		cleanHist[i] = m
	}
	return aiChatRecord{
		ID:          c.ID,
		Scope:       scope,
		Title:       c.Title,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
		Turns:       turns,
		ChatHistory: cleanHist,
		Plan:        slices.Clone(c.Plan),
		Explanation: c.Explanation,
		Command:     c.Command,
		Commands:    slices.Clone(c.Commands),
	}
}

func recordToConversation(rec aiChatRecord) *aiConversation {
	turns := make([]aiTurn, 0, len(rec.Turns))
	for _, st := range rec.Turns {
		steps := make([]*aiStepRecord, 0, len(st.Steps))
		for _, ss := range st.Steps {
			tools := make([]*aiToolRecord, 0, len(ss.Tools))
			for _, tl := range ss.Tools {
				tools = append(tools, &aiToolRecord{
					ID:      tl.ID,
					Name:    tl.Name,
					Summary: tl.Summary,
					Args:    tl.Args,
					Output:  tl.Output,
					Status:  aiToolStatus(tl.Status),
				})
			}
			steps = append(steps, &aiStepRecord{
				Step:            ss.Step,
				Thinking:        ss.Thinking,
				ThinkingSeconds: ss.ThinkingSeconds,
				Content:         ss.Content,
				Tools:           tools,
			})
		}
		turns = append(turns, aiTurn{
			query:       st.Query,
			prompt:      st.Prompt,
			attachedIDs: slices.Clone(st.AttachedIDs),
			extraErr:    st.ExtraErr,
			contextTags: slices.Clone(st.ContextTags),
			steps:       steps,
			usage:       st.Usage,
			err:         st.Err,
			explanation: st.Explanation,
			commands:    slices.Clone(st.Commands),
		})
	}
	return &aiConversation{
		ID:           rec.ID,
		Title:        rec.Title,
		CreatedAt:    rec.CreatedAt,
		UpdatedAt:    rec.UpdatedAt,
		Turns:        turns,
		ChatHistory:  slices.Clone(rec.ChatHistory),
		Plan:         slices.Clone(rec.Plan),
		PlanExpanded: len(rec.Plan) > 0,
		Explanation:  rec.Explanation,
		Command:      rec.Command,
		Commands:     slices.Clone(rec.Commands),
	}
}

func (a *app) persistAIConversation(tabID int, c *aiConversation) {
	a.syncAIVaultState()
	if c == nil || c.ID == "" || len(c.Turns) == 0 || a.aiVault == nil || a.aiVault.State() != vault.Unlocked {
		return
	}
	scope := a.aiScopeKeyForTabID(tabID)
	rec := conversationToRecord(scope, c)
	if err := a.aiVault.Apply([]vault.Change{aiChatChange(rec)}); err != nil {
		return
	}
	if idx := slices.IndexFunc(a.aiChats, func(x aiChatRecord) bool { return x.ID == rec.ID }); idx >= 0 {
		a.aiChats[idx] = rec
	} else {
		a.aiChats = append(a.aiChats, rec)
	}
	slices.SortStableFunc(a.aiChats, func(x, y aiChatRecord) int {
		if cmpRes := y.UpdatedAt.Compare(x.UpdatedAt); cmpRes != 0 {
			return cmpRes
		}
		if cmpRes := y.CreatedAt.Compare(x.CreatedAt); cmpRes != 0 {
			return cmpRes
		}
		return cmp.Compare(x.ID, y.ID)
	})
	a.prunePersistedAIChats()
	a.noteAIVaultChanged()

	syncTarget := func(s *aiPanel, sTabID int) {
		if s == nil || sTabID == tabID || a.aiScopeKeyForTabID(sTabID) != scope {
			return
		}
		existing := s.convByID(rec.ID)
		if existing == nil {
			s.convs = append(s.convs, recordToConversation(rec))
			sortAIConversations(s.convs)
			return
		}
		if existing.Loading {
			return
		}
		cp := recordToConversation(rec)
		*existing = *cp
		if s.convID == rec.ID {
			loadConversationInto(s, existing)
		}
		sortAIConversations(s.convs)
	}
	syncTarget(&a.ai, a.aiActiveTabID)
	for tid, s := range a.aiSessions {
		if tid != a.aiActiveTabID {
			syncTarget(s, tid)
		}
	}
}

func (a *app) deletePersistedAIConversation(id string) {
	a.syncAIVaultState()
	if id == "" || a.aiVault == nil || a.aiVault.State() != vault.Unlocked {
		return
	}
	inMem := slices.ContainsFunc(a.aiChats, func(x aiChatRecord) bool { return x.ID == id })
	_, inVault, _ := a.aiVault.Get(id)
	if !inMem && !inVault {
		return
	}
	if err := a.aiVault.Apply([]vault.Change{{Kind: kindAIChat, ID: id, Delete: true}}); err != nil {
		return
	}
	a.aiChats = slices.DeleteFunc(a.aiChats, func(x aiChatRecord) bool { return x.ID == id })
	a.noteAIVaultChanged()

	dropFrom := func(s *aiPanel) {
		if s == nil {
			return
		}
		if idx := slices.IndexFunc(s.convs, func(c *aiConversation) bool { return c != nil && c.ID == id }); idx >= 0 {
			stopAIConversation(s.convs[idx])
			s.convs = slices.Delete(s.convs, idx, idx+1)
		}
		if s.convID == id {
			stopActiveAIConversation(s)
			resetActiveConversationDraft(s, s.targetOS)
		}
	}
	for tid, s := range a.aiSessions {
		if tid != a.aiActiveTabID {
			dropFrom(s)
		}
	}
}

// prunePersistedAIChats 限制独立 AI 分片中会话历史的数量上限（单作用域最多 15 条、全局最多 60 条），避免分片体积无限膨胀。
func (a *app) prunePersistedAIChats() {
	const (
		maxPerScope = 15
		maxTotal    = 60
	)
	if a.aiVault == nil || a.aiVault.State() != vault.Unlocked || len(a.aiChats) <= maxPerScope {
		return
	}
	scopeCount := make(map[string]int)
	var changes []vault.Change
	dropIDs := make(map[string]bool)
	kept := 0
	for _, rec := range a.aiChats {
		scopeCount[rec.Scope]++
		if scopeCount[rec.Scope] > maxPerScope || kept >= maxTotal {
			dropIDs[rec.ID] = true
			changes = append(changes, vault.Change{Kind: kindAIChat, ID: rec.ID, Delete: true})
			continue
		}
		kept++
	}
	if len(changes) == 0 {
		return
	}
	if err := a.aiVault.Apply(changes); err != nil {
		return
	}
	a.aiChats = slices.DeleteFunc(a.aiChats, func(x aiChatRecord) bool { return dropIDs[x.ID] })
	dropFrom := func(s *aiPanel) {
		if s == nil {
			return
		}
		s.convs = slices.DeleteFunc(s.convs, func(c *aiConversation) bool {
			return c != nil && dropIDs[c.ID] && c.ID != s.convID && !c.Loading
		})
	}
	dropFrom(&a.ai)
	for tid, s := range a.aiSessions {
		if tid != a.aiActiveTabID {
			dropFrom(s)
		}
	}
}

func (a *app) mergePersistedConvsInto(s *aiPanel, scope string) {
	if s == nil || len(a.aiChats) == 0 {
		return
	}
	seen := make(map[string]bool, len(s.convs))
	for _, c := range s.convs {
		if c != nil && c.ID != "" {
			seen[c.ID] = true
		}
	}
	added := false
	for _, rec := range a.aiChats {
		if rec.Scope != scope || rec.ID == "" || seen[rec.ID] {
			continue
		}
		s.convs = append(s.convs, recordToConversation(rec))
		seen[rec.ID] = true
		added = true
	}
	if added {
		sortAIConversations(s.convs)
	}
}

func (a *app) hydrateAISessionsFromVault() {
	a.mergePersistedConvsInto(&a.ai, a.aiScopeKeyForTabID(a.aiActiveTabID))
	for tid, s := range a.aiSessions {
		if tid != a.aiActiveTabID {
			a.mergePersistedConvsInto(s, a.aiScopeKeyForTabID(tid))
		}
	}
}

func (a *app) flushAIConversationsOnExit() {
	saveActiveConversation(&a.ai)
	flushPanel := func(s *aiPanel, tabID int) {
		if s == nil {
			return
		}
		for _, c := range s.convs {
			if c != nil && c.Loading && len(c.Turns) > 0 {
				a.persistAIConversation(tabID, c)
			}
		}
	}
	flushPanel(&a.ai, a.aiActiveTabID)
	for tid, s := range a.aiSessions {
		if tid != a.aiActiveTabID {
			flushPanel(s, tid)
		}
	}
}

// toggleAI 切换 AI Agent 侧栏展开与收起，不丢失已有对话。
func (a *app) toggleAI() {
	a.syncTabDrawers()
	if a.ai.open {
		a.ai.open = false
		return
	}
	a.openAI()
}

// clearAIChat 清空当前正在查看的对话并停止该对话进行中的请求（保留其他历史会话）。
func (a *app) clearAIChat() {
	a.syncAITab()
	if a.ai.convID != "" {
		a.deleteAIConversation(a.ai.convID)
		a.ai.showHistory = false
		return
	}
	a.stopAIRequest()
	resetActiveConversationDraft(&a.ai, a.activeTargetOS())
}

// openAIExplain 打开 AI Agent 并立即分析传入的报错文本。
func (a *app) openAIExplain(errorText string) {
	a.openAI()
	a.ai.contextError = strings.TrimSpace(errorText)
	if a.ai.contextError != "" {
		a.attachAIContext("terminal")
		a.runAIExplain()
	}
}

// openAIExplainFromActivePane 从最近复制内容或当前活动终端屏幕提取报错文本并启动分析。
func (a *app) openAIExplainFromActivePane() {
	text := strings.TrimSpace(a.lastCopiedText)
	if text == "" {
		text = a.captureActiveTerminalTail(60)
	}
	a.openAIExplain(text)
}

func (a *app) captureActiveTerminalTail(maxLines int) string {
	t := a.current()
	if t == nil || t.focus == nil || t.focus.term == nil {
		return ""
	}
	return captureTermTail(t.focus.term.Text(), maxLines)
}

func (a *app) attachAIContext(id string) {
	for _, existing := range a.ai.attachedContexts {
		if existing == id {
			return
		}
	}
	a.ai.attachedContexts = append(a.ai.attachedContexts, id)
}

func (a *app) removeAIContext(id string) {
	var out []string
	for _, existing := range a.ai.attachedContexts {
		if existing != id {
			out = append(out, existing)
		}
	}
	a.ai.attachedContexts = out
}

func mentionLabel(id string) string {
	for _, opt := range aiMentionOptions {
		if opt.ID == id {
			return "@" + opt.Label
		}
	}
	return "@" + id
}

func (a *app) buildAttachedContextBlock(snap aiSessionSnapshot, attached []string, extraErr string) (tags []string, block string) {
	var parts []string
	seen := map[string]bool{}
	for _, id := range attached {
		if seen[id] {
			continue
		}
		seen[id] = true
		switch id {
		case "terminal":
			tags = append(tags, "@终端输出")
			tail := strings.TrimSpace(extraErr)
			if tail == "" {
				if t := a.targetTabForSnapshot(snap); t != nil && t.focus != nil && t.focus.term != nil {
					tail = captureTermTail(t.focus.term.Text(), 80)
				}
			}
			if tail != "" {
				if !a.local.DisableAIRedact {
					tail, _ = ai.RedactSecrets(tail)
				}
				parts = append(parts, "[附加终端输出]\n"+tail)
			}
		case "host":
			tags = append(tags, "@主机信息")
			if snap.hasTab {
				parts = append(parts, fmt.Sprintf("[附加主机信息]\n名称=%s 地址=%s:%d 用户=%s 目录=%s 系统=%s 生产环境=%v",
					snap.hostName, snap.hostAddr, snap.hostPort, snap.liveUser, snap.liveDir, snap.distro, snap.production))
			}
		case "sftp":
			tags = append(tags, "@SFTP目录")
			if snap.sftpDir != "" {
				parts = append(parts, "[附加 SFTP 目录]\n当前 SFTP 路径="+snap.sftpDir)
			} else if snap.liveDir != "" {
				parts = append(parts, "[附加 SFTP 目录]\n当前工作目录="+snap.liveDir)
			}
		case "clipboard":
			tags = append(tags, "@最近复制")
			if clip := strings.TrimSpace(a.lastCopiedText); clip != "" {
				if !a.local.DisableAIRedact {
					clip, _ = ai.RedactSecrets(clip)
				}
				parts = append(parts, "[附加最近复制内容]\n"+clip)
			}
		}
	}
	if extraErr != "" && !seen["terminal"] {
		if !a.local.DisableAIRedact {
			extraErr, _ = ai.RedactSecrets(extraErr)
		}
		parts = append(parts, "[终端报错输出]\n"+extraErr)
	}
	return tags, strings.Join(parts, "\n\n")
}

// compressChatHistory 对早期工具返回消息做摘要截断，避免长会话把大量 12~32KB 工具输出持续塞入上下文窗口。
func compressChatHistory(msgs []ai.Message) []ai.Message {
	if len(msgs) <= 6 {
		return msgs
	}
	cutoff := len(msgs) - 6
	out := make([]ai.Message, len(msgs))
	for i, m := range msgs {
		if i < cutoff && m.Role == "tool" && len(m.Content) > 600 {
			runes := []rune(m.Content)
			if len(runes) > 300 {
				m.Content = string(runes[:300]) + "\n...[早期工具输出已摘要折叠]"
			}
		}
		out[i] = m
	}
	return out
}

// runAIGenerate 发起一轮多步流式 Agent 执行。
func (a *app) runAIGenerate() {
	a.syncAITab()
	query := strings.TrimSpace(a.ai.query)
	if query == "" {
		return
	}
	a.startAgentRun(query, query, a.ai.attachedContexts, "")
	a.ai.query = ""
	a.ai.attachedContexts = nil
	a.ai.showMentions = false
	a.ai.showSlash = false
}

// runAIExplain 异步调用 AI 分析终端报错。
func (a *app) runAIExplain() {
	a.syncAITab()
	errText := strings.TrimSpace(a.ai.contextError)
	if errText == "" {
		return
	}
	a.startAgentRun("分析终端报错", "请分析以下终端报错输出的原因，如需要可继续检查主机状态并给出修复建议。", []string{"terminal"}, errText)
	a.ai.contextError = ""
}

func (a *app) startAgentRun(displayQuery, promptText string, attached []string, extraErr string) {
	a.startAgentRunAt(displayQuery, promptText, attached, extraErr, -1)
}

func (a *app) startAgentRunAt(displayQuery, promptText string, attached []string, extraErr string, replaceTurnIdx int) {
	a.syncAITab()
	targetTabID := a.currentAITabID()
	a.ai.showHistory = false

	if a.ai.cancel != nil {
		a.ai.cancel()
		a.ai.cancel = nil
	}
	if a.ai.pendingApproval != nil {
		if a.ai.pendingApproval.tool != nil && a.ai.pendingApproval.tool.Status == toolWaitingApproval {
			a.ai.pendingApproval.tool.Status = toolDenied
		}
		select {
		case a.ai.pendingApproval.answer <- aiApprovalDecision{}:
		default:
		}
		a.ai.pendingApproval = nil
	}

	client := a.aiClient()
	ctx, cancel := context.WithCancel(context.Background())

	a.ai.reqSeq++
	seq := a.ai.reqSeq
	a.ai.loading = true
	a.ai.cancel = cancel
	a.ai.err = ""
	a.ai.autoScroll = true

	snap := a.captureAISessionSnapshot()
	mode := a.ai.mode
	redact := !a.local.DisableAIRedact
	tags, ctxBlock := a.buildAttachedContextBlock(snap, attached, extraErr)

	userContent := promptText
	if ctxBlock != "" {
		userContent = ctxBlock + "\n\n用户请求：" + promptText
	}

	if replaceTurnIdx >= 0 && replaceTurnIdx < len(a.ai.turns) {
		hs := a.ai.turns[replaceTurnIdx].historyStart
		if hs >= 0 && hs <= len(a.ai.chatHistory) {
			a.ai.chatHistory = slices.Clone(a.ai.chatHistory[:hs])
		}
	}
	historyStart := len(a.ai.chatHistory)

	cfg := a.activeAIConfig()
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(cfg.Name)
	}

	newTurn := aiTurn{
		query:        displayQuery,
		prompt:       promptText,
		attachedIDs:  slices.Clone(attached),
		extraErr:     extraErr,
		contextTags:  tags,
		running:      true,
		startedAt:    time.Now(),
		modelName:    modelName,
		historyStart: historyStart,
	}
	turnIdx := len(a.ai.turns)
	if replaceTurnIdx >= 0 && replaceTurnIdx < len(a.ai.turns) {
		turnIdx = replaceTurnIdx
		a.ai.turns[turnIdx] = newTurn
	} else {
		a.ai.turns = append(a.ai.turns, newTurn)
	}
	saveActiveConversation(&a.ai)
	targetConvID := a.ai.convID
	curConv := a.ai.convByID(targetConvID)
	if curConv != nil && curConv.OutputStore == nil {
		curConv.OutputStore = ai.NewToolOutputStore()
	}
	var turnStore *ai.ToolOutputStore
	var allowedProgs map[string]bool
	if curConv != nil {
		turnStore = curConv.OutputStore
		allowedProgs = maps.Clone(curConv.SessionAllowedProgs)
	}
	if turnStore == nil {
		turnStore = ai.NewToolOutputStore()
	}

	sysMsg := ai.Message{Role: "system", Content: agentSystemPrompt(snap, mode)}
	compressed := compressChatHistory(a.ai.chatHistory)
	history := make([]ai.Message, 0, len(compressed)+2)
	history = append(history, sysMsg)
	history = append(history, compressed...)
	history = append(history, ai.Message{Role: "user", Content: userContent})
	tools := agentToolDefinitions()

	updateConv := func(fn func(c *aiConversation)) {
		sess := a.aiSessionForTab(targetTabID)
		c := sess.convByID(targetConvID)
		if c == nil || seq != c.ReqSeq {
			return
		}
		fn(c)
		c.UpdatedAt = time.Now()
		if sess.convID == targetConvID {
			sess.turns = c.Turns
			sess.chatHistory = c.ChatHistory
			sess.plan = c.Plan
			sess.planExpanded = c.PlanExpanded
			sess.loading = c.Loading
			sess.cancel = c.Cancel
			sess.err = c.Err
			sess.explanation = c.Explanation
			sess.command = c.Command
			sess.commands = c.Commands
			sess.pendingApproval = c.PendingApproval
			if sess.scroll.MaxY <= 0 || sess.scroll.Y >= sess.scroll.MaxY-24 {
				sess.autoScroll = true
			}
		}
	}

	ensureStep := func(c *aiConversation, stepNum int) *aiStepRecord {
		if turnIdx >= len(c.Turns) {
			return nil
		}
		for _, s := range c.Turns[turnIdx].steps {
			if s.Step == stepNum {
				return s
			}
		}
		s := &aiStepRecord{Step: stepNum}
		c.Turns[turnIdx].steps = append(c.Turns[turnIdx].steps, s)
		return s
	}

	go func() {
		var mu sync.Mutex
		lastProgressPost := time.Time{}
		toolByID := map[string]*aiToolRecord{}
		turnState := &aiTurnExecState{
			outputStore:    turnStore,
			sessionAllowed: allowedProgs,
			redact:         redact,
		}
		defer turnState.close()

		hbDone := make(chan struct{})
		defer close(hbDone)
		if a.post != nil {
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-hbDone:
						return
					case <-ctx.Done():
						return
					case <-ticker.C:
						a.post(func() {})
					}
				}
			}()
		}

		res, err := ai.RunAgent(ctx, ai.AgentRunConfig{
			Client:           client,
			Messages:         history,
			Tools:            tools,
			MaxSteps:         20,
			EnableCompaction: true,
			IsReadOnlyTool:   isReadOnlyAgentTool,
			ExecuteToolStream: func(tctx context.Context, call ai.ToolCall, onProgress func(string)) (string, bool) {
				mu.Lock()
				rec := toolByID[call.ID]
				mu.Unlock()
				return a.executeAgentToolStream(tctx, targetConvID, seq, snap, mode, turnState, rec, call, onProgress)
			},
			OnEvent: func(ev ai.AgentEvent) {
				switch ev.Kind {
				case ai.EventCompacted:
					notice := ev.CompactedNotice
					a.post(func() {
						updateConv(func(c *aiConversation) {
							if turnIdx < len(c.Turns) {
								c.Turns[turnIdx].compactedNotice = notice
							}
						})
					})
				case ai.EventStepStart:
					stepNum := ev.Step
					a.post(func() {
						updateConv(func(c *aiConversation) {
							_ = ensureStep(c, stepNum)
						})
					})
				case ai.EventStepProgress:
					now := time.Now()
					mu.Lock()
					shouldPost := now.Sub(lastProgressPost) >= 45*time.Millisecond
					if shouldPost {
						lastProgressPost = now
					}
					mu.Unlock()
					if !shouldPost {
						return
					}
					stepNum := ev.Step
					th, thSec, ct, usage := ev.Thinking, ev.ThinkingSeconds, ev.Content, ev.TotalUsage
					a.post(func() {
						updateConv(func(c *aiConversation) {
							s := ensureStep(c, stepNum)
							if s == nil {
								return
							}
							s.Thinking = th
							s.ThinkingSeconds = thSec
							s.Content = ct
							c.Turns[turnIdx].usage = usage
						})
					})
				case ai.EventToolStart:
					summary, args := summarizeToolCall(ev.ToolCall)
					rec := &aiToolRecord{
						ID:        ev.ToolCall.ID,
						Name:      ev.ToolCall.Function.Name,
						Summary:   summary,
						Args:      args,
						Status:    toolRunning,
						StartedAt: time.Now(),
					}
					mu.Lock()
					toolByID[ev.ToolCall.ID] = rec
					mu.Unlock()
					stepNum := ev.Step
					a.post(func() {
						updateConv(func(c *aiConversation) {
							s := ensureStep(c, stepNum)
							if s == nil {
								return
							}
							s.Tools = append(s.Tools, rec)
						})
					})
				case ai.EventToolProgress:
					mu.Lock()
					rec := toolByID[ev.ToolCall.ID]
					mu.Unlock()
					partial := ev.ToolOutput
					a.post(func() {
						updateConv(func(c *aiConversation) {
							if rec == nil || rec.Status != toolRunning {
								return
							}
							rec.Output = partial
						})
					})
				case ai.EventToolDone:
					mu.Lock()
					rec := toolByID[ev.ToolCall.ID]
					mu.Unlock()
					out, isErr := ev.ToolOutput, ev.ToolError
					a.post(func() {
						updateConv(func(c *aiConversation) {
							if rec == nil {
								return
							}
							rec.Output = out
							if rec.Status != toolDenied {
								if isErr {
									rec.Status = toolError
								} else {
									rec.Status = toolDone
								}
							}
						})
					})
				case ai.EventStepDone:
					stepNum := ev.Step
					th, thSec, ct, usage := ev.Thinking, ev.ThinkingSeconds, ev.Content, ev.TotalUsage
					a.post(func() {
						updateConv(func(c *aiConversation) {
							s := ensureStep(c, stepNum)
							if s == nil {
								return
							}
							s.Thinking = th
							s.ThinkingSeconds = thSec
							s.Content = ct
							c.Turns[turnIdx].usage = usage
						})
					})
				}
			},
		})

		a.post(func() {
			canceled := ctx.Err() == context.Canceled
			cancel()
			if canceled {
				// 即使中途停止，也将已完成的工具调用与部分上下文保留并落盘，方便用户追问继续。
				sess := a.aiSessionForTab(targetTabID)
				if c := sess.convByID(targetConvID); c != nil {
					if res != nil && len(res.Messages) > 1 {
						c.ChatHistory = ai.SanitizeMessageSequence(res.Messages[1:])
					} else {
						c.ChatHistory = append(c.ChatHistory,
							ai.Message{Role: "user", Content: userContent},
							ai.Message{Role: "assistant", Content: "（该轮回复已被用户中止）"},
						)
					}
					if sess.convID == targetConvID {
						sess.chatHistory = c.ChatHistory
					}
					a.persistAIConversation(targetTabID, c)
				}
				return
			}
			updateConv(func(c *aiConversation) {
				c.Loading = false
				c.Cancel = nil
				c.PendingApproval = nil
				if turnIdx < len(c.Turns) {
					c.Turns[turnIdx].running = false
				}
				if err != nil {
					msg := friendlyAIError(err)
					c.Err = msg
					if turnIdx < len(c.Turns) {
						c.Turns[turnIdx].err = msg
					}
					a.persistAIConversation(targetTabID, c)
					return
				}
				finishConversationTurn(c, turnIdx, userContent, res)
				a.persistAIConversation(targetTabID, c)
			})
		})
	}()
}

func finishConversationTurn(c *aiConversation, turnIdx int, userContent string, res *ai.AgentRunResult) {
	if c == nil || res == nil {
		return
	}
	parsed := ai.ParseMarkdown(res.FinalText)
	var cmds []string
	var explain string
	// 如果模型（或测试桩）返回经典 JSON {"explanation":..., "command":...}，转换为可渲染 Markdown
	if parsedResult := ai.ParseCommandText(res.FinalText); parsedResult != nil && (parsedResult.Command != "" || parsedResult.Explanation != "") {
		if strings.HasPrefix(strings.TrimSpace(res.FinalText), "{") {
			var md strings.Builder
			if parsedResult.Explanation != "" {
				md.WriteString(parsedResult.Explanation)
			}
			for _, cmd := range parsedResult.Commands {
				if md.Len() > 0 {
					md.WriteString("\n\n")
				}
				md.WriteString("```bash\n" + cmd + "\n```")
			}
			if turnIdx < len(c.Turns) {
				steps := c.Turns[turnIdx].steps
				if len(steps) > 0 {
					steps[len(steps)-1].Content = md.String()
				}
			}
			explain = parsedResult.Explanation
			cmds = parsedResult.Commands
		}
	}
	if len(cmds) == 0 {
		for _, blk := range parsed {
			if blk.Kind == ai.BlockCode && strings.TrimSpace(blk.Code) != "" {
				cmds = append(cmds, strings.TrimSpace(blk.Code))
			} else if blk.Kind == ai.BlockParagraph && explain == "" {
				explain = blk.Text
			}
		}
		if explain == "" {
			explain = res.FinalText
		}
	}

	c.Explanation = explain
	c.Commands = cmds
	if len(cmds) > 0 {
		c.Command = cmds[0]
	} else {
		c.Command = ""
	}
	if turnIdx < len(c.Turns) {
		c.Turns[turnIdx].explanation = explain
		c.Turns[turnIdx].commands = cmds
		c.Turns[turnIdx].usage = res.Usage
		c.Turns[turnIdx].reachedMax = res.ReachedMax
		if res.ReachedMax {
			c.Turns[turnIdx].err = "已达到单轮最大工具调用步数（20 步），可点击「继续执行」或追问让我深入排查。"
		}
	}

	// 将本轮消息写入上下文历史（保留多轮 tool/assistant 记录，剥离临时 system 提示）
	if len(res.Messages) > 1 {
		c.ChatHistory = append([]ai.Message(nil), res.Messages[1:]...)
	} else {
		c.ChatHistory = append(c.ChatHistory,
			ai.Message{Role: "user", Content: userContent},
			ai.Message{Role: "assistant", Content: res.FinalText},
		)
	}
}

func (a *app) stopAIRequest() {
	a.syncAITab()
	if c := a.ai.convByID(a.ai.convID); c != nil {
		stopAIConversation(c)
	}
	a.ai.reqSeq++
	if a.ai.cancel != nil {
		a.ai.cancel()
		a.ai.cancel = nil
	}
	if a.ai.pendingApproval != nil {
		if a.ai.pendingApproval.tool != nil && a.ai.pendingApproval.tool.Status == toolWaitingApproval {
			a.ai.pendingApproval.tool.Status = toolDenied
		}
		select {
		case a.ai.pendingApproval.answer <- aiApprovalDecision{}:
		default:
		}
		a.ai.pendingApproval = nil
	}
	a.ai.loading = false
	for i := range a.ai.turns {
		a.ai.turns[i].running = false
	}
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
	a.toast(ui.Toast{Title: "已将命令填入终端，请核对后回车执行"})
}

func (a *app) submitAIInput() {
	a.syncAITab()
	if a.ai.loading {
		a.toast(ui.Toast{Title: "AI 正在回复中，请等待完成或点击「停止」"})
		return
	}
	q := strings.TrimSpace(a.ai.query)
	if strings.HasPrefix(q, "/") {
		for _, sc := range aiSlashCommands {
			if strings.EqualFold(q, sc.Cmd) {
				a.runSlashCommand(sc)
				return
			}
		}
	}
	if len(a.ai.turns) == 0 && a.ai.contextError != "" && q == "" {
		a.runAIExplain()
		return
	}
	a.runAIGenerate()
}

func (a *app) runSlashCommand(sc aiSlashCommand) {
	a.ai.showSlash = false
	a.ai.query = ""
	if sc.IsNew {
		a.newAIConversation()
		return
	}
	if sc.IsClear {
		a.clearAIChat()
		return
	}
	for _, att := range sc.Attach {
		a.attachAIContext(att)
	}
	a.startAgentRun(sc.Cmd+" · "+sc.Title, sc.Prompt, a.ai.attachedContexts, "")
	a.ai.attachedContexts = nil
}

// aiDrawer 在右侧渲染可拖拽调节宽度的 AI Agent 停靠面板。
func (a *app) aiDrawer(c *ui.Context, colors design.Colors, width, maxW float32) {
	a.syncAITab()
	p := &a.ai
	if !p.open {
		return
	}
	if a.aiWidth <= 0 {
		a.aiWidth = 460
	}
	if width <= 0 {
		width = a.aiWidth
	}
	if maxW < 340 {
		maxW = 340
	}

	// 左侧拖拽手柄（不做宽度动画，避免终端连续收到 SIGWINCH）
	grip := ui.Box(c.Key("aiDrawerResize")).Width(8).Shrink(0).Margin(design.Gap/2, -4).
		Cursor(ui.CursorResizeEW).Focusable().Label("调整 AI 面板宽度")
	dx, _, held := grip.Dragged()
	changed := false
	switch {
	case grip.DoubleClicked():
		a.aiWidth = 460
		changed = true
	case held && dx != 0:
		a.aiWidth = min(max(width-dx, 340), maxW)
		a.aiResizing = true
	case a.aiResizing && !held:
		a.aiResizing = false
		changed = true
	case grip.Shortcut(0, ui.KeyLeft):
		a.aiWidth = min(width+20, maxW)
		changed = true
	case grip.Shortcut(0, ui.KeyRight):
		a.aiWidth = max(width-20, 340)
		changed = true
	}
	if changed && a.devicePath != "" {
		next := a.local
		next.AIWidth = a.aiWidth
		_ = a.saveLocal(next)
	}
	if held || grip.Hovered() {
		accent := c.Theme().Accent
		grip.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + r.W/2 - 0.5, Y: r.Y + 8, W: 1, H: max(0, r.H-16)}, accent, 0)
		})
	}

	drawer := raised(ui.Column(c.Key("aiDrawer")).Width(width).Shrink(0).Margin(design.Gap/2).Clip(), colors, design.RadiusCard)
	drawer.Transition(design.SlideIn(18)).Children(func() {
		a.aiDrawerHeader(c, colors)
		if p.showHistory {
			if p.pendingApproval != nil {
				a.aiApprovalCard(c, colors, p.pendingApproval)
			}
			a.aiHistoryView(c, colors)
		} else {
			if len(p.plan) > 0 {
				a.aiPlanBar(c, colors)
			}
			a.aiMessageStream(c, colors)
			if p.pendingApproval != nil {
				a.aiApprovalCard(c, colors, p.pendingApproval)
			}
			a.aiComposer(c, colors)
		}
	})

	// 代码块填入高危命令的弹窗二次确认（默认按钮放取消）
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

func (a *app) aiPlanBar(c *ui.Context, colors design.Colors) {
	p := &a.ai
	if len(p.plan) == 0 {
		return
	}
	t := c.Theme()
	total := len(p.plan)
	done, failed := 0, 0
	activeTitle := ""
	for _, st := range p.plan {
		switch st.Status {
		case planCompleted:
			done++
		case planFailed:
			failed++
		case planInProgress:
			if activeTitle == "" {
				activeTitle = st.Title
			}
		}
	}
	barColor := t.Accent
	if done == total {
		barColor = colors.Live
	} else if failed > 0 {
		barColor = colors.Danger
	}
	hasActive := activeTitle != ""

	ui.Column(c.Key("aiPlanBar")).Padding(8, 12).Gap(6).Background(colors.Hover).
		BorderColor(colors.Hairline).BorderWidth(0, 0, 1, 0).Shrink(0).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			toggle := ui.ButtonBase(c.Key("aiPlanToggle")).Label("展开或折叠执行计划").
				Grow(1).MinWidth(0).OnClick(func() {
				p.planExpanded = !p.planExpanded
				if conv := p.convByID(p.convID); conv != nil {
					conv.PlanExpanded = p.planExpanded
				}
			})
			toggle.Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
					ui.Icon(c, design.Icon("activity")).FontSize(13).TextColor(barColor).Shrink(0)
					ui.Text(c, "执行计划").FontSize(design.TextSmall).FontWeight(600).Shrink(0)
					if !p.planExpanded && activeTitle != "" {
						ui.Text(c, "· "+activeTitle).FontSize(design.TextSmall - 0.5).TextColor(t.TextMuted).
							SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
					} else {
						ui.Spacer(c)
					}
					ui.Text(c, fmt.Sprintf("%d/%d", done, total)).FontSize(design.TextSmall - 0.5).
						TextColor(barColor).FontFeatures("tnum").Shrink(0)
					chev := "chevron-right"
					if p.planExpanded {
						chev = "chevron-down"
					}
					ui.Icon(c, design.Icon(chev)).FontSize(12).TextColor(colors.TextFaint).Shrink(0)
				})
			})
			iconButton(c.Key("aiPlanClearBtn"), colors, "x", "清除执行计划", func() {
				p.plan = nil
				p.planExpanded = false
				if conv := p.convByID(p.convID); conv != nil {
					conv.Plan = nil
					conv.PlanExpanded = false
					a.persistAIConversation(a.currentAITabID(), conv)
				}
			}).Tooltip("清除执行计划")
		})

		// 4px 细进度条
		ui.Box(c).Height(4).Radius(2).Background(colors.Chip).Draw(func(painter *ui.Painter, r ui.Rect) {
			if r.W <= 0 || r.H <= 0 || total <= 0 {
				return
			}
			fillW := r.W * float32(done) / float32(total)
			if done == 0 && hasActive {
				fillW = min(r.W*0.08, 24)
			}
			if fillW > 0 {
				painter.Fill(ui.Rect{X: r.X, Y: r.Y, W: min(fillW, r.W), H: r.H}, barColor, 2)
			}
		})

		if p.planExpanded {
			ui.Scroll(c.Key("aiPlanScroll")).MaxHeight(160).Children(func() {
				ui.Column(c).Gap(4).Padding(2, 0, 0, 0).Children(func() {
					for _, st := range p.plan {
						step := st
						ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
							switch step.Status {
							case planCompleted:
								ui.Box(c).Size(7, 7).Radius(4).Background(colors.Live).Shrink(0)
							case planInProgress:
								loading.Arc(c, loading.Options{Size: 11, Color: t.Accent}).Label("进行中").Shrink(0)
							case planFailed:
								ui.Box(c).Size(7, 7).Radius(4).Background(colors.Danger).Shrink(0)
							default:
								ui.Box(c).Size(7, 7).Radius(4).Border(1, colors.TextFaint).Shrink(0)
							}
							titleEl := ui.Text(c, step.Title).FontSize(design.TextSmall).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
							switch step.Status {
							case planInProgress:
								titleEl.FontWeight(600).TextColor(t.Text)
							case planFailed:
								titleEl.TextColor(colors.Danger)
							default:
								titleEl.TextColor(t.TextMuted)
							}
							if step.Note != "" {
								ui.Text(c, step.Note).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).
									SingleLine().Ellipsis("…").MaxWidth(140).Shrink(0)
							}
						})
					}
				})
			})
		}
	})
}

func (a *app) aiDrawerHeader(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	ui.Row(c).Padding(10, 12).Gap(8).AlignItems(ui.Center).BorderColor(colors.Hairline).BorderWidth(0, 0, 1, 0).Shrink(0).Children(func() {
		ui.Icon(c, design.Icon("sparkles")).FontSize(15).TextColor(t.Accent)
		ui.Text(c, "AI Agent").FontSize(design.TextBody).FontWeight(600)

		targetText := "未连接终端"
		isProd := false
		if cur := a.current(); cur != nil {
			targetText = tabLabel(cur)
			if cur.host.Production {
				isProd = true
			}
		}
		chipBg, chipFg := colors.Chip, t.TextMuted
		if isProd {
			chipBg, chipFg = colors.DangerSoft, colors.Danger
			targetText = "生产 · " + targetText
		}
		ui.Box(c).Padding(2, 8).Radius(design.RadiusChip).Background(chipBg).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, targetText).FontSize(design.TextSmall).TextColor(chipFg).SingleLine().Ellipsis("…")
		})
		if a.ai.loading {
			ui.Row(c.Key("aiHeaderRunning")).Padding(2, 7).Gap(5).Radius(design.RadiusChip).
				Background(t.Accent.Alpha(0.12)).AlignItems(ui.Center).Shrink(0).Children(func() {
				loading.Pulse(c, loading.Options{Size: 10, Color: t.Accent}).Label("AI 执行中")
				ui.Text(c, "执行中").FontSize(design.TextSmall - 1).FontWeight(500).TextColor(t.Accent).NoWrap()
			})
		}
		ui.Spacer(c)

		histBtn := iconButton(c.Key("aiHistoryBtn"), colors, "history", locales.AiHistoryTitle(), func() {
			saveActiveConversation(&a.ai)
			a.ai.showHistory = !a.ai.showHistory
		})
		if a.ai.showHistory {
			histBtn.Background(colors.Hover)
		}
		if len(a.ai.turns) > 0 || a.ai.showHistory {
			iconButton(c.Key("aiNewChatBtn"), colors, "plus", locales.AiNewChat(), a.newAIConversation)
		}
		iconButton(c.Key("aiCloseBtn"), colors, "x", locales.AiClosePanel(), func() {
			a.ai.open = false
		})
	})
}

func (a *app) aiHistoryView(c *ui.Context, colors design.Colors) {
	p := &a.ai
	t := c.Theme()
	ui.Column(c).Grow(1).MinHeight(0).Padding(12).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
			ui.Text(c, fmt.Sprintf("本终端历史会话（%d）", len(p.convs))).
				FontSize(design.TextBody).FontWeight(600).Grow(1).MinWidth(0)
			ui.Button(c.Key("aiHistoryNewBtn"), "新建会话").Height(26).Padding(0, 10).OnClick(a.newAIConversation)
			ui.Button(c.Key("aiHistoryBackBtn"), "返回对话").Height(26).Padding(0, 10).OnClick(func() {
				p.showHistory = false
			})
		})
		ui.Scroll(c.Key("aiHistoryScroll")).Grow(1).MinHeight(0).Children(func() {
			if len(p.convs) == 0 {
				ui.Box(c).Padding(14).Radius(design.RadiusRow).Background(colors.Hover).Children(func() {
					ui.Text(c, "当前终端暂无历史会话，发送消息后会自动记录在这里。").
						FontSize(design.TextSmall).TextColor(t.TextMuted)
				})
				return
			}
			ui.Column(c).Gap(6).Children(func() {
				for _, conv := range p.convs {
					if conv == nil {
						continue
					}
					item := conv
					isCur := item.ID == p.convID
					row := ui.Row(c).Gap(6).Padding(8, 10).Radius(design.RadiusRow).AlignItems(ui.Center)
					if isCur {
						row.Background(t.Accent.Alpha(0.12)).Border(1, t.Accent.Alpha(0.35))
					} else {
						row.Background(colors.Hover).Border(1, colors.Hairline)
					}
					row.Children(func() {
						if p.renamingConvID == item.ID {
							in := ui.TextInput(c.Key("aiRenameConvInput"), &p.renameConvDraft).
								Placeholder("输入会话名称").Grow(1).MinWidth(0)
							in.OnSubmit(func() {
								name := strings.TrimSpace(p.renameConvDraft)
								if name != "" {
									item.Title = name
									item.UpdatedAt = time.Now()
									if item.ID == p.convID {
										p.convTitle = name
									}
									a.persistAIConversation(a.currentAITabID(), item)
								}
								p.renamingConvID = ""
							})
							in.OnShortcut(0, ui.KeyEscape, func() {
								p.renamingConvID = ""
							})
							ui.Button(c.Key("aiRenameSaveBtn:"+item.ID), "保存").Height(24).Padding(0, 8).OnClick(func() {
								name := strings.TrimSpace(p.renameConvDraft)
								if name != "" {
									item.Title = name
									item.UpdatedAt = time.Now()
									if item.ID == p.convID {
										p.convTitle = name
									}
									a.persistAIConversation(a.currentAITabID(), item)
								}
								p.renamingConvID = ""
							})
							iconButton(c.Key("aiRenameCancelBtn:"+item.ID), colors, "x", "取消重命名", func() {
								p.renamingConvID = ""
							})
							return
						}
						openBtn := ui.ButtonBase(c.Key("aiConv:" + item.ID)).Label("打开会话 " + item.Title).
							Grow(1).MinWidth(0).OnClick(func() {
							a.selectAIConversation(item.ID)
						})
						openBtn.Children(func() {
							ui.Column(c).Gap(2).Children(func() {
								ui.Text(c, item.Title).FontSize(design.TextBody).FontWeight(600).
									TextColor(t.Text).SingleLine().Ellipsis("…")
								meta := fmt.Sprintf("%d 轮 · %s", len(item.Turns), item.UpdatedAt.Format("15:04"))
								if isCur {
									meta += " · 当前"
								}
								if item.Loading {
									meta += " · 进行中"
								}
								ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
									if item.Loading {
										loading.BouncingDots(c, loading.Options{Size: 10, Color: t.Accent}).Label("会话执行中").Shrink(0)
									}
									ui.Text(c, meta).FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
								})
							})
						})
						iconButton(c.Key("aiRenameConv:"+item.ID), colors, "pencil", "重命名会话 "+item.ID, func() {
							p.renamingConvID = item.ID
							p.renameConvDraft = item.Title
						}).Tooltip("重命名该会话")
						iconButton(c.Key("aiDelConv:"+item.ID), colors, "trash-2", "删除会话 "+item.ID, func() {
							a.deleteAIConversation(item.ID)
						}).Tooltip("删除该会话")
					})
				}
			})
		})
	})
}

func (a *app) aiMessageStream(c *ui.Context, colors design.Colors) {
	p := &a.ai
	t := c.Theme()
	if p.autoScroll {
		p.scroll.Y = math.MaxFloat32
		p.autoScroll = false
	}
	pinnedBottom := p.scroll.MaxY <= 0 || p.scroll.Y >= p.scroll.MaxY-24

	ui.Box(c).Grow(1).MinHeight(0).Clip().Children(func() {
		ui.Scroll(c.Key("aiScroll")).Fill().TrackScroll(&p.scroll).Children(func() {
			ui.Column(c).Padding(12).Gap(14).Children(func() {
				if len(p.turns) == 0 && p.contextError == "" {
					a.aiWelcomeView(c, colors)
				}

				if p.contextError != "" && len(p.turns) == 0 {
					ui.Column(c).Padding(10).Gap(6).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.Hairline).Children(func() {
						ui.Text(c, "已捕获终端输出上下文").FontSize(design.TextSmall).FontWeight(600).TextColor(t.Accent)
						ui.Text(c, p.contextError).FontSize(design.TextSmall).Font(design.FontTerminal).TextColor(t.TextMuted).Selectable()
					})
				}

				for i := range p.turns {
					a.renderAITurn(c, colors, i, &p.turns[i])
				}

				if p.err != "" && (len(p.turns) == 0 || p.turns[len(p.turns)-1].err == "") {
					ui.Text(c.Key("aiError"), p.err).TextColor(colors.Danger).FontSize(design.TextSmall).Selectable()
					if len(a.configuredAIProfiles()) == 0 {
						ui.Row(c).Justify(ui.Start).Children(func() {
							ui.Button(c.Key("aiGoSettingsBtn"), "前往配置模型").Height(26).Padding(0, 10).OnClick(a.openAISettings)
						})
					}
				}
			})
		})
		if p.scroll.Y > 2 {
			ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(18).PassThrough().AccessHidden(true).
				Material(glass.ScrollEdge{Background: colors.Card})
		}
		if p.scroll.MaxY > 0 && p.scroll.Y < p.scroll.MaxY-2 {
			ui.Box(c).Absolute().Bottom(0).Left(0).Right(0).Height(18).PassThrough().AccessHidden(true).
				Material(glass.ScrollEdge{Bottom: true, Background: colors.Card})
		}
		if !pinnedBottom && p.scroll.MaxY > 0 && len(p.turns) > 0 {
			btn := ui.ButtonBase(c.Key("aiScrollBottom")).Absolute().Right(14).Bottom(12).
				Label("回到底部").Tooltip("滚动到最新消息").Height(28).Padding(0, 12).Gap(5).Radius(14).Center().
				Material(glass.Glass{Interactive: true}).Border(1, colors.CardBorder).
				Shadow(0, 4, 14, -4, colors.CardShadow)
			btn.Children(func() {
				ui.Icon(c, design.Icon("arrow-down")).FontSize(12).TextColor(t.Accent)
				ui.Text(c, "回到底部").FontSize(design.TextSmall).FontWeight(500).TextColor(t.Text).NoWrap()
			}).OnClick(func() {
				p.autoScroll = true
				p.scroll.Y = math.MaxFloat32
			})
		}
	})
}

func (a *app) aiWelcomeView(c *ui.Context, colors design.Colors) {
	t := c.Theme()
	ui.Column(c).Gap(10).Padding(8, 2).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(2, 0, 4, 0).Children(func() {
			ui.Box(c).Size(30, 30).Radius(8).Background(t.Accent.Alpha(0.12)).Center().Shrink(0).Children(func() {
				ui.Icon(c, design.Icon("sparkles")).FontSize(15).TextColor(t.Accent)
			})
			ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, "AI 运维助手").FontSize(design.TextBody).FontWeight(600)
				ui.Text(c, "自动探测主机、读取终端并总结结论").FontSize(design.TextSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			})
		})
		if len(a.configuredAIProfiles()) == 0 {
			ui.Row(c).Padding(8, 10).Gap(8).AlignItems(ui.Center).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.Hairline).Children(func() {
				ui.Text(c, "尚未配置 AI 模型密钥，点击右侧配置后即可开始对话。").
					FontSize(design.TextSmall).TextColor(t.TextMuted).Grow(1).MinWidth(0)
				ui.Button(c.Key("aiWelcomeSettingsBtn"), "前往配置模型").Height(26).Padding(0, 10).Shrink(0).OnClick(a.openAISettings)
			})
		}
		ui.Text(c, "常用快捷排查：").
			FontSize(design.TextSmall).TextColor(colors.TextFaint)
		starters := []struct {
			key    string
			label  string
			desc   string
			prompt string
			attach []string
		}{
			{"starterInspect", "这台服务配置如何", "检查内核、CPU、内存、磁盘与监听端口", "这台服务器配置和当前运行状态如何？请主动检查系统内核、CPU、内存、磁盘、网络接口、关键进程与监听端口并汇总成表格。", []string{"host"}},
			{"starterDiagnose", "分析当前终端报错", "读取最近终端输出并定位根因与修复方案", "请读取当前终端最近的输出和报错信息，分析根因并给出排查与修复建议。", []string{"terminal", "host"}},
			{"starterPorts", "查看监听端口与进程", "列出端口、协议与对应服务进程占用", "请检查当前服务器正在监听的端口和主要进程占用情况，列出端口、协议与对应服务。", []string{"host"}},
			{"starterDisk", "排查磁盘与内存占用", "定位大目录、分区使用率与高内存进程", "请排查这台主机的磁盘分区使用率、大目录及内存占用最高的进程。", []string{"host"}},
		}
		for _, st := range starters {
			item := st
			b := ui.ButtonBase(c.Key(item.key)).Label(item.label).Padding(8, 10).Radius(design.RadiusRow).
				Background(colors.Hover).Border(1, colors.Hairline).Transition(design.Hover)
			if b.Hovered() {
				b.Background(colors.Selected)
			}
			b.Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
					ui.Icon(c, design.Icon("sparkles")).FontSize(13).TextColor(t.Accent).Shrink(0)
					ui.Column(c).Gap(1).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, item.label).FontSize(design.TextBody).FontWeight(500).SingleLine().Ellipsis("…")
						ui.Text(c, item.desc).FontSize(design.TextSmall - 0.5).TextColor(colors.TextFaint).SingleLine().Ellipsis("…")
					})
				})
			}).OnClick(func() {
				for _, att := range item.attach {
					a.attachAIContext(att)
				}
				a.startAgentRun(item.label, item.prompt, a.ai.attachedContexts, "")
				a.ai.attachedContexts = nil
			})
		}

		if len(a.ai.convs) > 0 {
			ui.Column(c).Gap(6).Padding(6, 0, 0, 0).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "最近会话").FontSize(design.TextSmall).FontWeight(600).TextColor(t.TextMuted).Grow(1)
					if len(a.ai.convs) > 3 {
						ui.Button(c.Key("aiWelcomeAllHistory"), "查看全部历史").Height(22).Padding(0, 8).OnClick(func() {
							saveActiveConversation(&a.ai)
							a.ai.showHistory = true
						})
					}
				})
				limit := min(3, len(a.ai.convs))
				for i := 0; i < limit; i++ {
					conv := a.ai.convs[i]
					if conv == nil {
						continue
					}
					item := conv
					rb := ui.ButtonBase(c.Key("aiRecentConv:"+item.ID)).Label("恢复会话 "+item.Title).
						Padding(7, 10).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.Hairline).Transition(design.Hover)
					if rb.Hovered() {
						rb.Background(colors.Selected)
					}
					rb.Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Icon(c, design.Icon("history")).FontSize(12).TextColor(t.TextMuted)
							ui.Text(c, item.Title).FontSize(design.TextSmall).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
							ui.Text(c, fmt.Sprintf("%d 轮", len(item.Turns))).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).Shrink(0)
						})
					}).OnClick(func() {
						a.selectAIConversation(item.ID)
					})
				}
			})
		}
	})
}

func turnAnswerText(turn *aiTurn) string {
	var parts []string
	for _, s := range turn.steps {
		if txt := strings.TrimSpace(s.Content); txt != "" {
			parts = append(parts, txt)
		}
	}
	if len(parts) == 0 && strings.TrimSpace(turn.explanation) != "" {
		parts = append(parts, strings.TrimSpace(turn.explanation))
	}
	return strings.Join(parts, "\n\n")
}

func aiTurnActivityStatus(turn *aiTurn, pendingApproval *aiApproval) (phase string, kind string) {
	if pendingApproval != nil {
		return "等待确认下方操作…", "approval"
	}
	if turn != nil && len(turn.steps) > 0 {
		last := turn.steps[len(turn.steps)-1]
		if last != nil {
			var runningTools []*aiToolRecord
			for _, tl := range last.Tools {
				if tl != nil && tl.Status == toolRunning {
					runningTools = append(runningTools, tl)
				}
			}
			if len(runningTools) > 1 {
				return fmt.Sprintf("正在并行执行 %d 项工具…", len(runningTools)), "tool"
			}
			if len(runningTools) == 1 {
				summary := strings.TrimSpace(runningTools[0].Summary)
				if summary == "" {
					summary = runningTools[0].Name
				}
				return "正在执行：" + summary, "tool"
			}
			if strings.TrimSpace(last.Content) != "" {
				return "正在生成回复…", "stream"
			}
			if strings.TrimSpace(last.Thinking) != "" {
				return fmt.Sprintf("第 %d 步 · 深度思考中…", max(last.Step, 1)), "think"
			}
			if len(turn.steps) > 1 {
				return fmt.Sprintf("第 %d 步 · 正在分析工具结果…", max(last.Step, 1)), "think"
			}
		}
	}
	return "正在连接模型并分析需求…", "connect"
}

func (a *app) renderAITurn(c *ui.Context, colors design.Colors, turnIdx int, turn *aiTurn) {
	t := c.Theme()
	ui.Column(c).Gap(10).Children(func() {
		// 用户消息气泡
		ui.Row(c).Justify(ui.End).Gap(6).AlignItems(ui.End).Children(func() {
			if !turn.running && !a.ai.loading && turn.prompt != "" {
				editPrompt, editAtt := turn.prompt, slices.Clone(turn.attachedIDs)
				iconButton(c.Key(fmt.Sprintf("aiEditTurn:%d", turnIdx)), colors, "pencil", "编辑重发", func() {
					a.ai.query = editPrompt
					a.ai.attachedContexts = editAtt
				}).Tooltip("填入输入框编辑后重发")
			}
			ui.Column(c).MaxWidth(340).Padding(8, 12).Gap(4).Radius(design.RadiusRow).
				Background(t.Accent.Alpha(0.13)).Border(1, t.Accent.Alpha(0.24)).Children(func() {
				if len(turn.contextTags) > 0 {
					ui.Row(c).Gap(4).Wrap().Children(func() {
						for _, tag := range turn.contextTags {
							ui.Box(c).Padding(1, 6).Radius(4).Background(colors.Card).Children(func() {
								ui.Text(c, tag).FontSize(design.TextSmall - 1).TextColor(t.Accent)
							})
						}
					})
				}
				ui.Text(c, turn.query).FontSize(design.TextBody).Selectable()
			})
		})

		hasAssistantBody := turn.running || len(turn.steps) > 0 || turn.explanation != "" || len(turn.commands) > 0 || turn.err != ""
		if hasAssistantBody {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, design.Icon("sparkles")).FontSize(13).TextColor(t.Accent).Shrink(0)
				ui.Text(c, "AI Agent").FontSize(design.TextSmall).FontWeight(600).TextColor(t.Text).Shrink(0)
				if turn.modelName != "" {
					ui.Text(c, turn.modelName).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).SingleLine().Ellipsis("…").MinWidth(0)
				}
			})
		}

		// 多轮推理与工具步骤
		if turn.compactedNotice != "" {
			ui.Row(c).Padding(4, 8).Radius(6).Background(t.Accent.Alpha(0.08)).Border(1, t.Accent.Alpha(0.22)).Children(func() {
				ui.Text(c, turn.compactedNotice).FontSize(design.TextSmall - 0.5).TextColor(t.Accent)
			})
		}
		for sIdx, step := range turn.steps {
			if strings.TrimSpace(step.Thinking) != "" {
				label := "正在思考…"
				if step.ThinkingSeconds > 0 {
					label = fmt.Sprintf("思考了 %ds", step.ThinkingSeconds)
				} else if !turn.running {
					label = "思考过程"
				}
				ui.Column(c).Padding(6, 10).Radius(design.RadiusRow).Background(colors.Hover).Children(func() {
					c.Key(fmt.Sprintf("aiThink:%d:%d", turnIdx, sIdx))
					ui.Collapsible(c, label, &step.ThinkExpanded, func() {
						ui.Text(c, step.Thinking).FontSize(design.TextSmall).TextColor(t.TextMuted).Selectable()
					})
				})
			}

			for tIdx, tool := range step.Tools {
				a.renderToolCard(c, colors, turnIdx, sIdx, tIdx, tool)
			}

			if strings.TrimSpace(step.Content) != "" {
				a.renderMarkdown(c, colors, turnIdx, sIdx, step.Content)
			}
		}

		// 兼容旧结构直接填充 explanation / commands 的情况
		if len(turn.steps) == 0 && !turn.running && (turn.explanation != "" || len(turn.commands) > 0) {
			var md strings.Builder
			if turn.explanation != "" {
				md.WriteString(turn.explanation)
			}
			for _, cmd := range turn.commands {
				if md.Len() > 0 {
					md.WriteString("\n\n")
				}
				md.WriteString("```bash\n" + cmd + "\n```")
			}
			a.renderMarkdown(c, colors, turnIdx, 0, md.String())
		}

		// 运行中始终展示明确阶段反馈与动态指示器（包括首包等待、思考停顿与工具间隙）
		if turn.running {
			var activeApproval *aiApproval
			if turnIdx == len(a.ai.turns)-1 {
				activeApproval = a.ai.pendingApproval
			}
			phase, kind := aiTurnActivityStatus(turn, activeApproval)
			ui.Row(c.Key(fmt.Sprintf("aiTurnStatus:%d", turnIdx))).Padding(7, 10).Gap(8).AlignItems(ui.Center).
				Radius(design.RadiusRow).Background(t.Accent.Alpha(0.08)).Border(1, t.Accent.Alpha(0.22)).Children(func() {
				switch kind {
				case "think":
					loading.Orbit(c, loading.Options{Size: 14, Color: t.Accent}).Label(phase).Shrink(0)
				case "tool":
					loading.Arc(c, loading.Options{Size: 13, Color: t.Accent}).Label(phase).Shrink(0)
				case "approval":
					loading.Pulse(c, loading.Options{Size: 13, Color: t.Accent}).Label(phase).Shrink(0)
				default:
					loading.BouncingDots(c, loading.Options{Size: 13, Color: t.Accent}).Label(phase).Shrink(0)
				}
				ui.Text(c, phase).FontSize(design.TextSmall).TextColor(t.Text).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				if !turn.startedAt.IsZero() {
					if sec := int(time.Since(turn.startedAt).Seconds()); sec > 0 {
						ui.Text(c, fmt.Sprintf("%ds", sec)).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
					}
				}
			})
		}

		if turn.err != "" {
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, turn.err).FontSize(design.TextSmall).TextColor(colors.Danger).Selectable()
				if !turn.running && !a.ai.loading {
					ui.Row(c).Gap(8).Children(func() {
						if turn.prompt != "" {
							retryPrompt, retryQuery, retryAtt, retryExtra := turn.prompt, turn.query, turn.attachedIDs, turn.extraErr
							replaceIdx := -1
							if turnIdx == len(a.ai.turns)-1 {
								replaceIdx = turnIdx
							}
							ui.Button(c.Key(fmt.Sprintf("aiRetryTurn:%d", turnIdx)), locales.AiRetry()).Height(24).Padding(0, 10).OnClick(func() {
								a.startAgentRunAt(retryQuery, retryPrompt, retryAtt, retryExtra, replaceIdx)
							})
						}
						if turn.reachedMax && turnIdx == len(a.ai.turns)-1 {
							ui.PrimaryButton(c.Key("aiContinueBtn"), "继续执行").Height(24).Padding(0, 10).OnClick(func() {
								a.startAgentRun("继续执行排查", "请基于上面的排查进度继续执行剩余步骤。", nil, "")
							})
						}
						if len(a.configuredAIProfiles()) == 0 {
							ui.Button(c.Key(fmt.Sprintf("aiTurnGoSettings:%d", turnIdx)), "前往配置模型").Height(24).Padding(0, 10).OnClick(a.openAISettings)
						}
					})
				}
			})
		}

		answerText := turnAnswerText(turn)
		if !turn.running && (turn.usage.TotalTokens > 0 || answerText != "") {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if turn.usage.TotalTokens > 0 {
					u := turn.usage
					usageLine := fmt.Sprintf("Token %s · 输入 %s · 输出 %s",
						formatIntComma(u.TotalTokens), formatIntComma(u.PromptTokens), formatIntComma(u.CompletionTokens))
					if u.CachedTokens > 0 {
						usageLine += " · 缓存 " + formatIntComma(u.CachedTokens)
					}
					if u.ReasoningTokens > 0 {
						usageLine += " · 推理 " + formatIntComma(u.ReasoningTokens)
					}
					ui.Text(c, usageLine).FontSize(design.TextSmall - 0.5).TextColor(colors.TextFaint).FontFeatures("tnum").Grow(1).MinWidth(0)
				} else {
					ui.Box(c).Grow(1)
				}
				if !a.ai.loading && turnIdx == len(a.ai.turns)-1 && turn.prompt != "" {
					regenPrompt, regenQuery, regenAtt, regenExtra := turn.prompt, turn.query, turn.attachedIDs, turn.extraErr
					ui.Button(c.Key(fmt.Sprintf("aiRegenerateTurn:%d", turnIdx)), "重新生成").Height(22).Padding(0, 8).Shrink(0).OnClick(func() {
						a.startAgentRunAt(regenQuery, regenPrompt, regenAtt, regenExtra, turnIdx)
					})
				}
				if answerText != "" {
					ui.Button(c.Key(fmt.Sprintf("aiCopyAnswer:%d", turnIdx)), locales.AiCopyAnswer()).Height(22).Padding(0, 8).Shrink(0).OnClick(func() {
						c.WriteClipboard(answerText)
						a.toast(ui.Toast{Title: "回答已复制到剪贴板"})
					})
				}
			})
		}
	})
}

func (a *app) renderToolCard(c *ui.Context, colors design.Colors, turnIdx, sIdx, tIdx int, tool *aiToolRecord) {
	t := c.Theme()
	dotColor := t.Accent
	statusText := "执行中"
	if tool.Status == toolRunning && !tool.StartedAt.IsZero() {
		if sec := int(time.Since(tool.StartedAt).Seconds()); sec >= 1 {
			statusText = fmt.Sprintf("执行中 %ds", sec)
		}
	}
	switch tool.Status {
	case toolWaitingApproval:
		dotColor = t.Accent
		statusText = "待确认"
	case toolDone:
		dotColor = colors.Live
		statusText = "完成"
	case toolError:
		dotColor = colors.Danger
		statusText = "异常"
	case toolDenied:
		dotColor = colors.Danger
		statusText = "已拒绝"
	}

	ui.Column(c).Padding(7, 10).Gap(6).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.Hairline).Children(func() {
		hdr := ui.ButtonBase(c.Key(fmt.Sprintf("aiTool:%d:%d:%d", turnIdx, sIdx, tIdx))).
			OnClick(func() {
				tool.Expanded = !tool.Expanded
			})
		hdr.Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				switch tool.Status {
				case toolRunning:
					loading.Arc(c, loading.Options{Size: 12, Color: t.Accent}).Label("工具执行中").Shrink(0)
				case toolWaitingApproval:
					loading.Pulse(c, loading.Options{Size: 12, Color: t.Accent}).Label("等待确认").Shrink(0)
				default:
					ui.Box(c).Size(7, 7).Radius(4).Background(dotColor).Shrink(0)
				}
				ui.Text(c, tool.Summary).FontSize(design.TextSmall).Font(design.FontTerminal).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				ui.Text(c, statusText).FontSize(design.TextSmall - 1).TextColor(colors.TextFaint).FontFeatures("tnum").Shrink(0)
				chev := "chevron-right"
				if tool.Expanded {
					chev = "chevron-down"
				}
				ui.Icon(c, design.Icon(chev)).FontSize(12).TextColor(colors.TextFaint)
			})
		})
		if tool.Status == toolRunning && !tool.Expanded && strings.TrimSpace(tool.Output) != "" {
			tail := captureTermTail(tool.Output, 5)
			if tail != "" {
				ui.Box(c).Padding(5, 8).Radius(6).Background(colors.Card).Border(1, colors.Hairline).Children(func() {
					ui.Text(c, tail).FontSize(design.TextSmall - 0.5).Font(design.FontTerminal).TextColor(t.TextMuted).Selectable()
				})
			}
		}
		if tool.Expanded {
			if strings.TrimSpace(tool.Args) != "" {
				ui.Text(c, tool.Args).FontSize(design.TextSmall - 0.5).Font(design.FontTerminal).TextColor(t.TextMuted).Selectable()
			}
			if strings.TrimSpace(tool.Output) != "" {
				out := tool.Output
				if len(out) > 2400 {
					out = out[:2400] + "\n...[展开预览已截断]"
				}
				ui.Box(c).Padding(6, 8).Radius(6).Background(colors.Card).Border(1, colors.Hairline).Children(func() {
					ui.Text(c, out).FontSize(design.TextSmall - 0.5).Font(design.FontTerminal).TextColor(t.TextMuted).Selectable()
				})
			}
		}
	})
}

type markdownRenderOptions struct {
	bodyFontSize    float32
	smallFontSize   float32
	headingFontSize float32
	blockGap        float32
	firstColWidth   float32
	renderCode      func(blk ai.MarkdownBlock)
}

func isShellCodeLang(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "sh", "bash", "zsh", "shell", "console", "fish", "ash", "dash":
		return true
	default:
		return false
	}
}

func (a *app) renderMarkdown(c *ui.Context, colors design.Colors, turnIdx, sIdx int, content string) {
	codeIdx := 0
	renderMarkdownBlocks(c, colors, content, markdownRenderOptions{
		bodyFontSize:    design.TextBody,
		smallFontSize:   design.TextSmall,
		headingFontSize: design.TextHeading,
		blockGap:        8,
		firstColWidth:   84,
		renderCode: func(blk ai.MarkdownBlock) {
			cmdVal := blk.Code
			isMultiline := strings.ContainsAny(cmdVal, "\r\n")
			isShell := isShellCodeLang(blk.Lang)
			curCodeIdx := codeIdx
			codeIdx++
			t := c.Theme()
			langLabel := strings.TrimSpace(blk.Lang)
			if langLabel == "" {
				if isShell {
					langLabel = "shell"
				} else {
					langLabel = "code"
				}
			}
			ui.Column(c).Radius(8).Background(colors.Card).Border(1, colors.CardBorder).Clip().Children(func() {
				ui.Row(c).Padding(5, 8).Gap(6).AlignItems(ui.Center).Background(colors.Hover).
					BorderColor(colors.Hairline).BorderWidth(0, 0, 1, 0).Children(func() {
					ui.Text(c, langLabel).FontSize(design.TextSmall - 1).Font(design.FontTerminal).
						TextColor(colors.TextFaint).SingleLine().Shrink(0)
					ui.Spacer(c)
					if isShell && isMultiline {
						ui.Text(c, "多行脚本请复制后核对执行").FontSize(design.TextSmall - 1).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
					}
					copyKey := fmt.Sprintf("aiCopyBtn:%d:%d:%d", turnIdx, sIdx, curCodeIdx)
					if turnIdx == len(a.ai.turns)-1 && curCodeIdx == 0 {
						copyKey = "aiCopyBtn"
					}
					copyLabel := locales.AiCopyCode()
					copyToast := "代码已复制到剪贴板"
					if isShell {
						copyLabel = locales.AiCopyCommand()
						copyToast = "命令已复制到剪贴板"
					}
					ui.Button(c.Key(copyKey), copyLabel).Height(22).Padding(0, 8).Shrink(0).OnClick(func() {
						c.WriteClipboard(cmdVal)
						a.toast(ui.Toast{Title: copyToast})
					})
					if isShell && !isMultiline {
						insertKey := fmt.Sprintf("aiInsertBtn:%d:%d:%d", turnIdx, sIdx, curCodeIdx)
						if turnIdx == len(a.ai.turns)-1 && curCodeIdx == 0 {
							insertKey = "aiInsertBtn"
						}
						ui.PrimaryButton(c.Key(insertKey), locales.AiInsertTerminal()).Height(22).Padding(0, 8).Shrink(0).OnClick(func() {
							a.insertCommandToTerminal(cmdVal)
						})
					}
				})
				ui.Box(c).Padding(8, 10).Children(func() {
					ui.Text(c, cmdVal).FontSize(design.TextSmall).Font(design.FontTerminal).Selectable()
				})
			})
		},
	})
}

var (
	mdCacheMu     sync.Mutex
	mdBlockCache  = map[string][]ai.MarkdownBlock{}
	mdInlineCache = map[string][]ai.InlineSpan{}
)

func cachedParseMarkdown(content string) []ai.MarkdownBlock {
	mdCacheMu.Lock()
	if blocks, ok := mdBlockCache[content]; ok {
		mdCacheMu.Unlock()
		return blocks
	}
	mdCacheMu.Unlock()

	blocks := ai.ParseMarkdown(content)

	mdCacheMu.Lock()
	if len(mdBlockCache) >= 256 {
		clear(mdBlockCache)
	}
	mdBlockCache[content] = blocks
	mdCacheMu.Unlock()
	return blocks
}

func cachedParseInlineMarkdown(s string) []ai.InlineSpan {
	mdCacheMu.Lock()
	if spans, ok := mdInlineCache[s]; ok {
		mdCacheMu.Unlock()
		return spans
	}
	mdCacheMu.Unlock()

	spans := ai.ParseInlineMarkdown(s)

	mdCacheMu.Lock()
	if len(mdInlineCache) >= 256 {
		clear(mdInlineCache)
	}
	mdInlineCache[s] = spans
	mdCacheMu.Unlock()
	return spans
}

func renderInlineMarkdown(c *ui.Context, colors design.Colors, raw string, fontSize float32, weight int, textColor ui.Color) ui.Element {
	s := replaceHTMLBreaks(raw)
	inlines := cachedParseInlineMarkdown(s)
	if len(inlines) == 0 {
		el := ui.Text(c, s).FontSize(fontSize).Selectable()
		if weight > 0 {
			el.FontWeight(weight)
		}
		if textColor.A > 0 {
			el.TextColor(textColor)
		}
		return el
	}
	accent := c.Theme().Accent
	spans := make([]ui.Span, 0, len(inlines))
	for _, sp := range inlines {
		switch sp.Kind {
		case ai.SpanBold:
			spans = append(spans, ui.Span{Text: sp.Text, Weight: 600})
		case ai.SpanCode:
			spans = append(spans, ui.Span{
				Text:       sp.Text,
				Font:       design.FontTerminal,
				Size:       max(fontSize-1, 10),
				Color:      accent,
				Background: colors.Hover,
				Radius:     4,
				PaddingX:   4,
				PaddingY:   1,
			})
		default:
			spans = append(spans, ui.Span{Text: sp.Text})
		}
	}
	el := ui.RichText(c, spans...).FontSize(fontSize).Selectable()
	if weight > 0 {
		el.FontWeight(weight)
	}
	if textColor.A > 0 {
		el.TextColor(textColor)
	}
	return el
}

func renderMarkdownBlocks(c *ui.Context, colors design.Colors, content string, opts markdownRenderOptions) {
	t := c.Theme()
	blocks := cachedParseMarkdown(content)
	if len(blocks) == 0 {
		ui.Text(c, replaceHTMLBreaks(content)).FontSize(opts.bodyFontSize).Selectable()
		return
	}
	ui.Column(c).Gap(opts.blockGap).Children(func() {
		for _, blk := range blocks {
			switch blk.Kind {
			case ai.BlockHeading:
				fs := opts.bodyFontSize + 1
				if blk.Level == 1 {
					fs = opts.headingFontSize
				}
				renderInlineMarkdown(c, colors, blk.Text, fs, 600, ui.Color{})
			case ai.BlockParagraph:
				renderInlineMarkdown(c, colors, blk.Text, opts.bodyFontSize, 0, ui.Color{})
			case ai.BlockQuote:
				ui.Row(c).Gap(8).Children(func() {
					ui.Box(c).Width(3).Background(t.Accent.Alpha(0.5)).Radius(2)
					renderInlineMarkdown(c, colors, blk.Text, opts.smallFontSize, 0, t.TextMuted).Grow(1).MinWidth(0)
				})
			case ai.BlockList:
				ui.Column(c).Gap(4).Padding(0, 0, 0, 4).Children(func() {
					for idx, item := range blk.Items {
						prefix := "• "
						if blk.Ordered {
							prefix = fmt.Sprintf("%d. ", idx+1)
						}
						renderInlineMarkdown(c, colors, prefix+item, opts.bodyFontSize, 0, ui.Color{})
					}
				})
			case ai.BlockDivider:
				ui.Box(c).Height(1).Background(colors.Hairline)
			case ai.BlockTable:
				renderMarkdownTable(c, colors, blk.Headers, blk.Rows, opts.firstColWidth)
			case ai.BlockCode:
				if opts.renderCode != nil {
					opts.renderCode(blk)
				} else {
					ui.Box(c).Padding(8, 10).Radius(6).Background(colors.Card).Border(1, colors.CardBorder).Children(func() {
						ui.Text(c, blk.Code).FontSize(opts.smallFontSize).Font(design.FontTerminal).Selectable()
					})
				}
			}
		}
	})
}

func renderMarkdownTable(c *ui.Context, colors design.Colors, headers []string, rows [][]string, firstColWidth float32) {
	if len(headers) == 0 {
		return
	}
	if firstColWidth <= 0 {
		firstColWidth = 84
	}
	colWeight := func(colIdx int) float32 {
		if len(headers) == 2 && colIdx == 0 {
			return 0
		}
		return 1
	}
	cellContainer := func(colIdx int, header bool, txt string) {
		cell := ui.Box(c).Padding(6, 8).MinWidth(0)
		if colWeight(colIdx) == 0 {
			cell.Width(firstColWidth).Shrink(0)
		} else {
			cell.Grow(1)
		}
		if colIdx > 0 {
			cell.BorderColor(colors.Hairline).BorderWidth(0, 0, 0, 1)
		}
		cell.Children(func() {
			w := 0
			if header {
				w = 600
			}
			renderInlineMarkdown(c, colors, txt, design.TextSmall, w, ui.Color{})
		})
	}

	ui.Column(c).Radius(6).Background(colors.Card).Border(1, colors.CardBorder).Clip().Children(func() {
		ui.Row(c).Background(colors.Hover).BorderColor(colors.Hairline).BorderWidth(0, 0, 1, 0).AlignItems(ui.Stretch).Children(func() {
			for i, h := range headers {
				cellContainer(i, true, h)
			}
		})
		for rIdx, row := range rows {
			rEl := ui.Row(c).AlignItems(ui.Stretch)
			if rIdx < len(rows)-1 {
				rEl.BorderColor(colors.Hairline).BorderWidth(0, 0, 1, 0)
			}
			rEl.Children(func() {
				for i := range headers {
					val := ""
					if i < len(row) {
						val = row[i]
					}
					cellContainer(i, false, val)
				}
			})
		}
	})
}

func cleanInlineMarkdown(s string) string {
	s = replaceHTMLBreaks(s)
	spans := ai.ParseInlineMarkdown(s)
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	for _, sp := range spans {
		b.WriteString(sp.Text)
	}
	return b.String()
}

// replaceHTMLBreaks 将 GFM 表格与段落中的 <br> 换行标记转为真实换行，并折叠连续空行标签。
func replaceHTMLBreaks(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	for _, tag := range []string{"<br />", "<br/>", "<br>", "<BR />", "<BR/>", "<BR>"} {
		s = strings.ReplaceAll(s, tag, "\n")
	}
	for strings.Contains(s, "\n\n") {
		s = strings.ReplaceAll(s, "\n\n", "\n")
	}
	return strings.TrimSpace(s)
}

// aiApprovalCmdMaxHeight 根据窗口高度计算审批卡片内命令预览区的最大高度，避免长脚本把确认按钮挤出视口。
func aiApprovalCmdMaxHeight(windowH float32) float32 {
	if windowH <= 0 {
		return 220
	}
	return max(min(min(windowH*0.32, 260), windowH-360), 72)
}

func (a *app) aiApprovalCard(c *ui.Context, colors design.Colors, ap *aiApproval) {
	t := c.Theme()
	bg := colors.Hover
	border := t.Accent.Alpha(0.45)
	title := "请求执行命令确认"
	approveLabel := locales.AiApproveExec()
	if ap.kind == approvalFileWrite {
		title = "修改文件确认"
		approveLabel = "允许写入"
	}
	titleColor := t.Accent
	if ap.dangerous || ap.production {
		bg = colors.DangerSoft
		border = colors.Danger.Alpha(0.55)
		titleColor = colors.Danger
		if ap.kind == approvalFileWrite {
			if ap.dangerous {
				title = "高危文件修改确认"
			} else {
				title = "生产环境文件修改确认"
			}
		} else if ap.dangerous {
			title = "高危命令拦截确认"
		} else {
			title = "生产环境命令确认"
		}
	}
	_, windowH := c.Size()
	cmdMaxH := aiApprovalCmdMaxHeight(windowH)
	ui.Column(c).Margin(0, 10, 6, 10).Padding(10).Gap(8).Radius(design.RadiusRow).Background(bg).Border(1, border).Shrink(0).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(design.TextSmall).FontWeight(600).TextColor(titleColor).Grow(1).MinWidth(0)
			if ap.target != "" {
				ui.Text(c, ap.target).FontSize(design.TextSmall - 0.5).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			}
			copyContent := ap.command
			if ap.diff != "" {
				copyContent = ap.diff
			}
			ui.Button(c.Key("aiApprovalCopyBtn"), "复制").Height(20).Padding(0, 7).Shrink(0).OnClick(func() {
				c.WriteClipboard(copyContent)
				a.toast(ui.Toast{Title: "已复制到剪贴板"})
			})
			if ap.kind == approvalExec {
				editLabel := "编辑"
				if ap.editing {
					editLabel = "完成编辑"
				}
				ui.Button(c.Key("aiApprovalEditBtn"), editLabel).Height(20).Padding(0, 7).Shrink(0).OnClick(func() {
					ap.editing = !ap.editing
				})
			}
		})
		if ap.reason != "" {
			ui.Text(c, ap.reason).FontSize(design.TextSmall).TextColor(t.TextMuted)
		}
		if ap.filePath != "" {
			ui.Text(c, ap.filePath).FontSize(design.TextSmall - 0.5).Font(design.FontTerminal).TextColor(t.Accent).SingleLine().Ellipsis("…")
		}
		ui.Box(c).Radius(6).Background(colors.Card).Border(1, colors.CardBorder).Clip().Children(func() {
			ui.Scroll(c.Key("aiApprovalCmdScroll")).
				MaxHeight(cmdMaxH).
				TrackScroll(&ap.scroll).
				KeepScrollbars(true).
				Children(func() {
					ui.Box(c).Padding(6, 8).Children(func() {
						switch {
						case ap.editing && ap.kind == approvalExec:
							ui.TextArea(c.Key("aiApprovalEditArea"), &ap.command).
								Height(max(cmdMaxH-16, 96)).
								Font(design.FontTerminal).
								FontSize(design.TextSmall)
						case ap.diff != "":
							ui.Column(c).Gap(1).Children(func() {
								for _, line := range strings.Split(ap.diff, "\n") {
									col := t.Text
									switch {
									case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
										col = colors.Live
									case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
										col = colors.Danger
									case strings.HasPrefix(line, "@@"):
										col = t.Accent
									case strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++"):
										col = t.TextMuted
									}
									ui.Text(c, line).FontSize(design.TextSmall - 0.5).Font(design.FontTerminal).TextColor(col).Selectable()
								}
							})
						default:
							ui.Text(c, ap.command).FontSize(design.TextSmall).Font(design.FontTerminal).Selectable()
						}
					})
				})
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			ui.Button(c.Key("aiDenyBtn"), locales.AiDenyExec()).Height(26).Padding(0, 12).OnClick(func() {
				a.resolveAgentApproval(ap, aiApprovalDecision{})
			})
			if !ap.dangerous && ap.kind == approvalExec && ap.baseProg != "" {
				ui.Button(c.Key("aiAllowSessionBtn"), "本会话允许 "+ap.baseProg).Height(26).Padding(0, 10).OnClick(func() {
					a.resolveAgentApproval(ap, aiApprovalDecision{
						approved:     true,
						editedCmd:    ap.command,
						allowSession: true,
					})
				})
			}
			if ap.dangerous || ap.production {
				btn := ui.Button(c.Key("aiApproveBtn"), "").Label(approveLabel).Height(26).Padding(0, 12).
					Background(colors.DangerSoft).Border(1, colors.Danger.Alpha(0.55)).
					OnClick(func() {
						a.resolveAgentApproval(ap, aiApprovalDecision{
							approved:  true,
							editedCmd: ap.command,
						})
					})
				btn.Children(func() {
					ui.Text(c, approveLabel).FontWeight(600).TextColor(colors.Danger)
				})
			} else {
				ui.PrimaryButton(c.Key("aiApproveBtn"), approveLabel).Height(26).Padding(0, 12).OnClick(func() {
					a.resolveAgentApproval(ap, aiApprovalDecision{
						approved:  true,
						editedCmd: ap.command,
					})
				})
			}
		})
	})
}

func (a *app) resolveAgentApproval(ap *aiApproval, dec aiApprovalDecision) {
	if a.ai.pendingApproval != ap {
		return
	}
	a.ai.pendingApproval = nil
	if c := a.ai.convByID(a.ai.convID); c != nil && c.PendingApproval == ap {
		c.PendingApproval = nil
	}
	select {
	case ap.answer <- dec:
	default:
	}
}

func aiMentionToken(q string) (start int, needle string, matched bool) {
	idx := strings.LastIndexByte(q, '@')
	if idx < 0 {
		return 0, "", false
	}
	if idx > 0 {
		prev := q[idx-1]
		if prev != ' ' && prev != '\t' && prev != '\n' && prev != '\r' {
			return 0, "", false
		}
	}
	tail := q[idx+1:]
	if strings.ContainsAny(tail, " \t\r\n") {
		return 0, "", false
	}
	return idx, strings.ToLower(tail), true
}

func matchingSlashCommands(q string) []aiSlashCommand {
	qTrim := strings.ToLower(strings.TrimSpace(q))
	var out []aiSlashCommand
	for _, sc := range aiSlashCommands {
		if strings.HasPrefix(q, "/") && len(q) > 1 && !strings.HasPrefix(strings.ToLower(sc.Cmd), qTrim) {
			continue
		}
		out = append(out, sc)
	}
	return out
}

func matchingMentionOptions(mentionMatched bool, needle string) []aiMentionOption {
	var out []aiMentionOption
	for _, opt := range aiMentionOptions {
		if mentionMatched && needle != "" &&
			!strings.Contains(strings.ToLower(opt.ID), needle) &&
			!strings.Contains(strings.ToLower(opt.Label), needle) {
			continue
		}
		out = append(out, opt)
	}
	return out
}

func aiComposerHeight(q string) float32 {
	if q == "" {
		return 54
	}
	lines := 0
	for _, seg := range strings.Split(q, "\n") {
		n := len([]rune(seg))
		wrapped := max(1, (n+35)/36)
		lines += wrapped
	}
	return min(max(float32(lines)*18+16, 54), 140)
}

func (a *app) aiComposer(c *ui.Context, colors design.Colors) {
	p := &a.ai
	t := c.Theme()

	_, mentionNeedle, mentionMatched := aiMentionToken(p.query)
	showSlash := p.showSlash || strings.HasPrefix(p.query, "/")
	showMentions := p.showMentions || mentionMatched
	slashItems := matchingSlashCommands(p.query)
	mentionItems := matchingMentionOptions(mentionMatched, mentionNeedle)

	if showSlash && len(slashItems) > 0 {
		if p.popupIdx < 0 {
			p.popupIdx = 0
		} else if p.popupIdx >= len(slashItems) {
			p.popupIdx = len(slashItems) - 1
		}
	} else if showMentions && len(mentionItems) > 0 {
		if p.popupIdx < 0 {
			p.popupIdx = 0
		} else if p.popupIdx >= len(mentionItems) {
			p.popupIdx = len(mentionItems) - 1
		}
	} else {
		p.popupIdx = 0
	}

	ui.Column(c).Padding(6, 8, 8, 8).Gap(6).BorderColor(colors.Hairline).BorderWidth(1, 0, 0, 0).Shrink(0).Children(func() {
		if showSlash {
			ui.Column(c).Padding(6).Gap(2).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.CardBorder).Children(func() {
				for idx, sc := range slashItems {
					item := sc
					row := ui.ButtonBase(c.Key("aiSlash:"+item.Cmd)).Padding(5, 8).Radius(6).Justify(ui.Start).AlignItems(ui.Stretch).Transition(design.Hover)
					if row.Hovered() || idx == p.popupIdx {
						row.Background(colors.Selected)
					}
					row.Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
							ui.Text(c, item.Cmd).FontSize(design.TextSmall).Font(design.FontTerminal).TextColor(t.Accent).Shrink(0)
							ui.Text(c, item.Title).FontSize(design.TextSmall).FontWeight(500).Shrink(0)
							ui.Text(c, item.Desc).FontSize(design.TextSmall - 1).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
						})
					}).OnClick(func() {
						a.runSlashCommand(item)
					})
				}
				if len(slashItems) == 0 {
					ui.Text(c, "没有匹配的快捷指令").FontSize(design.TextSmall).TextColor(t.TextMuted).Padding(4, 8)
				}
			})
		}

		if showMentions {
			ui.Column(c).Padding(6).Gap(2).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.CardBorder).Children(func() {
				for idx, opt := range mentionItems {
					item := opt
					row := ui.ButtonBase(c.Key("aiMention:"+item.ID)).Padding(5, 8).Radius(6).Justify(ui.Start).AlignItems(ui.Stretch).Transition(design.Hover)
					if row.Hovered() || idx == p.popupIdx {
						row.Background(colors.Selected)
					}
					row.Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
							ui.Text(c, "@"+item.Label).FontSize(design.TextSmall).TextColor(t.Accent).FontWeight(500).Shrink(0)
							ui.Text(c, item.Desc).FontSize(design.TextSmall - 1).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
						})
					}).OnClick(func() {
						if start, _, ok := aiMentionToken(p.query); ok {
							p.query = p.query[:start]
						}
						a.attachAIContext(item.ID)
						p.showMentions = false
					})
				}
				if len(mentionItems) == 0 {
					ui.Text(c, "没有匹配的上下文引用").FontSize(design.TextSmall).TextColor(t.TextMuted).Padding(4, 8)
				}
			})
		}

		// 已附加的 @ 上下文标签
		if len(p.attachedContexts) > 0 {
			ui.Row(c).Gap(6).Wrap().Children(func() {
				for _, id := range p.attachedContexts {
					ctxID := id
					chip := ui.Row(c).Padding(2, 6).Gap(4).AlignItems(ui.Center).Radius(design.RadiusChip).
						Background(t.Accent.Alpha(0.12)).Border(1, t.Accent.Alpha(0.28))
					chip.Children(func() {
						ui.Text(c, mentionLabel(ctxID)).FontSize(design.TextSmall - 0.5).TextColor(t.Accent)
						ui.ButtonBase(c.Key("aiDropCtx:"+ctxID)).Label("移除 "+mentionLabel(ctxID)).Size(14, 14).Center().Children(func() {
							ui.Icon(c, design.Icon("x")).FontSize(10).TextColor(t.Accent)
						}).OnClick(func() {
							a.removeAIContext(ctxID)
						})
					})
				}
			})
		}

		// 输入卡片：多行文本框（Enter 发送，Shift+Enter 换行）+ 底部工具行
		ui.Column(c).Padding(7, 8).Gap(6).Radius(design.RadiusRow).Background(colors.Hover).Border(1, colors.CardBorder).Children(func() {
			c.Key("aiQueryInput")
			placeholder := "向 AI Agent 发送消息（Enter 发送，Shift+Enter 换行），@ 引用，/ 快捷命令"
			if p.pendingApproval != nil {
				placeholder = "请先确认或拒绝上方的命令执行请求"
			} else if p.contextError != "" && len(p.turns) == 0 {
				placeholder = "按回车直接诊断报错，或补充说明你想排查的问题"
			}
			in := ui.TextArea(c, &p.query).Height(aiComposerHeight(p.query)).Placeholder(placeholder)
			if p.fresh {
				in.AutoFocus()
				p.fresh = false
			}
			in.OnTextKey(func(k ui.TextKey) bool {
				cur := k.Text()
				_, needle, mMatched := aiMentionToken(cur)
				slashOpen := p.showSlash || strings.HasPrefix(cur, "/")
				mentionOpen := p.showMentions || mMatched
				if k.Key == ui.KeyEscape && (slashOpen || mentionOpen) {
					p.showMentions = false
					p.showSlash = false
					p.popupIdx = 0
					return true
				}
				if slashOpen {
					items := matchingSlashCommands(cur)
					if len(items) > 0 {
						switch k.Key {
						case ui.KeyUp:
							p.popupIdx = (p.popupIdx - 1 + len(items)) % len(items)
							return true
						case ui.KeyDown:
							p.popupIdx = (p.popupIdx + 1) % len(items)
							return true
						case ui.KeyTab, ui.KeyEnter:
							if k.Mods == 0 {
								idx := min(max(p.popupIdx, 0), len(items)-1)
								p.query = cur
								a.runSlashCommand(items[idx])
								k.SetText(p.query)
								p.popupIdx = 0
								return true
							}
						}
					}
				} else if mentionOpen {
					items := matchingMentionOptions(mMatched, needle)
					if len(items) > 0 {
						switch k.Key {
						case ui.KeyUp:
							p.popupIdx = (p.popupIdx - 1 + len(items)) % len(items)
							return true
						case ui.KeyDown:
							p.popupIdx = (p.popupIdx + 1) % len(items)
							return true
						case ui.KeyTab, ui.KeyEnter:
							if k.Mods == 0 {
								idx := min(max(p.popupIdx, 0), len(items)-1)
								if start, _, ok := aiMentionToken(cur); ok {
									cur = cur[:start]
									k.SetText(cur)
									p.query = cur
								}
								a.attachAIContext(items[idx].ID)
								p.showMentions = false
								p.popupIdx = 0
								return true
							}
						}
					}
				}
				if k.Key == ui.KeyEnter && k.Mods == 0 {
					p.query = cur
					a.submitAIInput()
					k.SetText(p.query)
					return true
				}
				return false
			})

			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Button(c.Key("aiMentionBtn"), "@").Label("引用上下文").Tooltip("@ 引用终端或主机上下文").
					Height(24).Padding(0, 7).OnClick(func() {
					p.showMentions = !p.showMentions
					p.showSlash = false
					p.popupIdx = 0
				})
				ui.Button(c.Key("aiSlashBtn"), "/").Label("快捷指令").Tooltip("/ 快捷运维指令").
					Height(24).Padding(0, 7).OnClick(func() {
					p.showSlash = !p.showSlash
					p.showMentions = false
					p.popupIdx = 0
				})

				cfg := a.activeAIConfig()
				configured := a.configuredAIProfiles()
				modelLabel := "选择 AI 模型"
				if len(configured) == 0 && len(a.settings.AIProfiles) > 0 {
					modelLabel = "未启用 AI 模型"
				} else if cfg.Name != "" && cfg.Model != "" {
					modelLabel = cfg.Name + " · " + cfg.Model
				} else if cfg.Name != "" {
					modelLabel = cfg.Name + " · 未选模型"
				} else if cfg.Model != "" {
					modelLabel = cfg.Model
				}
				c.Key("aiModelSwitchBtn")
				ui.PopoverMenuButton(c, modelLabel, func(m *ui.Menu) {
					hasItems := false
					switch len(configured) {
					case 0:
						// 未启用任何提供商时仅显示配置入口
					case 1:
						prof := configured[0]
						models := a.modelsForProfile(prof.ID, prof.Models, prof.Model)
						isCur := cfg.ID == prof.ID
						if len(models) == 0 {
							hasItems = true
							if m.Item(prof.Name + "（前往获取模型）").ID("provider:" + prof.ID).Chosen() {
								a.openAISettings()
							}
						}
						for _, modelName := range models {
							mv := modelName
							hasItems = true
							if m.Item(prof.Name + " · " + mv).ID("model:" + prof.ID + ":" + mv).Radio(isCur && cfg.Model == mv).Chosen() {
								_ = a.switchAIProviderModel(prof.ID, mv)
							}
						}
					default:
						for _, prof := range configured {
							item := prof
							isCur := cfg.ID == item.ID
							models := a.modelsForProfile(item.ID, item.Models, item.Model)
							hasItems = true
							switch {
							case len(models) > 1:
								sub := m.Submenu(item.Name, func(sm *ui.Menu) {
									for _, modelName := range models {
										mv := modelName
										if sm.Item(mv).ID("model:" + item.ID + ":" + mv).Radio(isCur && cfg.Model == mv).Chosen() {
											_ = a.switchAIProviderModel(item.ID, mv)
										}
									}
								}).ID("provider:" + item.ID)
								if isCur {
									sub.Trailing("当前")
								}
							case len(models) == 1:
								mv := models[0]
								if m.Item(item.Name + " · " + mv).ID("provider:" + item.ID).Radio(isCur).Chosen() {
									_ = a.switchAIProviderModel(item.ID, mv)
								}
							default:
								if m.Item(item.Name).ID("provider:" + item.ID).Radio(isCur).Chosen() {
									_ = a.switchAIProfile(item.ID)
								}
							}
						}
					}
					if hasItems {
						m.Separator()
					}
					if m.Item("配置模型与密钥…").ID("openAISettings").Chosen() {
						a.openAISettings()
					}
				}, menuButtonOptions(colors)).Height(24).Padding(0, 8).MaxWidth(185).Shrink(1).MinWidth(0).Tooltip(modelLabel)

				ui.Box(c).Grow(1)

				c.Key("aiModeSwitchBtn")
				ui.PopoverMenuButton(c, p.mode.label(), func(m *ui.Menu) {
					for _, md := range []aiPermissionMode{aiModeObserver, aiModeConfirm, aiModeAuto} {
						modeVal := md
						if m.Item(modeVal.label() + " — " + modeVal.description()).Radio(p.mode == modeVal).Chosen() {
							p.mode = modeVal
							if a.devicePath != "" {
								next := a.local
								next.AIMode = uint8(modeVal)
								_ = a.saveLocal(next)
							}
						}
					}
				}, menuButtonOptions(colors)).Height(24).Padding(0, 8).Shrink(0)

				if p.loading {
					stopBtn := ui.Button(c.Key("aiStopBtn"), "").Label(locales.AiStop()).Height(26).Padding(0, 10).
						Background(colors.DangerSoft).Border(1, colors.Danger.Alpha(0.45)).OnClick(a.stopAIRequest)
					stopBtn.Children(func() {
						ui.Text(c, locales.AiStop()).FontSize(design.TextSmall).FontWeight(600).TextColor(colors.Danger).NoWrap()
					})
				} else {
					sendLabel := locales.AiSubmit()
					if len(p.turns) == 0 && p.contextError != "" && strings.TrimSpace(p.query) == "" {
						sendLabel = locales.AiAnalyzeError()
					}
					ui.PrimaryButton(c.Key("aiSubmitBtn"), sendLabel).Height(26).Padding(0, 12).OnClick(a.submitAIInput)
				}
			})
		})
	})
}

func formatIntComma(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	rem := len(s) % 3
	if rem > 0 {
		out = append(out, s[:rem]...)
	}
	for i := rem; i < len(s); i += 3 {
		if len(out) > 0 {
			out = append(out, ',')
		}
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}
