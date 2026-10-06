// team.go — 团队、成员与 TeamManager（in-process 简化版）。
//
// 备注 internal/teams/teams.go + teamfile.go + registry.go
// 差异：只支持 in-process 后端，去掉 PaneID/tmux/iTerm 字段；成员运行时
// 从 Code 的 agent.Agent + conversation.Manager 换成 Eino 的
// ChatModelAgent 配置 + []*schema.Message 历史；名称注册表退化为
// name == agentID 的直接映射（同进程内不需要跨进程解析），整体并入
// SendMessage 的查找逻辑。

package teams

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"kcode/internal/todo"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// LeadName is the conventional sender/recipient identifier used by the
// coordinator side. Teammates send idle notifications here and read the
// lead's task assignments from messages with From == LeadName.
const LeadName = "lead"

// teamsBaseDir 是所有团队目录的根。放在用户主目录而不是项目目录下，
// 保证 Lead 和队员找到的永远是同一份团队配置。
func teamsBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		wd, _ := os.Getwd()
		home = wd
	}
	return filepath.Join(home, ".kcode", "teams")
}

// Member 是一个团队成员。运行态字段（ChatModel/Tools/History）只在
// in-process 循环里使用；其余元信息落盘，供跨会话恢复和 UI 展示。
type Member struct {
	Name string

	// Eino 运行时：SpawnTeammate 钩子注入。
	ChatModel   model.BaseModel[*schema.Message]
	Tools       []tool.BaseTool
	Instruction string
	Handlers    []adk.ChatModelAgentMiddleware
	MaxTurns    int

	// History 是队友的跨轮对话（Eino State 的替代品）：user prompt →
	// assistant(tool_calls) → tool → assistant …
	History []*schema.Message

	Active   bool
	Cancel   context.CancelFunc
	Progress *TeammateProgress

	// 落盘用的元信息，运行时不参与调度。
	AgentID      string
	AgentType    string
	Model        string
	WorktreePath string
	JoinedAt     int64
}

type Team struct {
	Name    string
	Mode    TeamMode
	Members map[string]*Member
	MailBox *FileMailBox
	mu      sync.Mutex

	// 落盘用的团队级元信息
	LeadAgentID string
	Description string
	CreatedAt   int64
}

func NewTeam(name string, mode TeamMode) *Team {
	inboxDir := filepath.Join(teamDir(name), "inboxes")
	return &Team{
		Name:      name,
		Mode:      mode,
		Members:   make(map[string]*Member),
		MailBox:   NewFileMailBox(inboxDir),
		CreatedAt: time.Now().Unix(),
	}
}

// AddMember 注册一个带 Eino 运行时配置的成员并落盘。
func (t *Team) AddMember(name string, chatModel model.BaseModel[*schema.Message], tools []tool.BaseTool, instruction string, handlers []adk.ChatModelAgentMiddleware, maxTurns int) *Member {
	t.mu.Lock()
	defer t.mu.Unlock()

	member := &Member{
		Name:        name,
		ChatModel:   chatModel,
		Tools:       tools,
		Instruction: instruction,
		Handlers:    handlers,
		MaxTurns:    maxTurns,
		Active:      false,
		AgentID:     name,
		JoinedAt:    time.Now().Unix(),
	}
	t.Members[name] = member
	t.persist()
	return member
}

// SetMemberMeta 补齐成员的元信息（agent 类型、模型、worktree 路径）并落盘。
func (t *Team) SetMemberMeta(name, agentType, model_, worktreePath string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	member, ok := t.Members[name]
	if !ok {
		return
	}
	member.AgentType = agentType
	member.Model = model_
	member.WorktreePath = worktreePath
	t.persist()
}

// StopMember 取消队友的运行循环并标记空闲。
func (t *Team) StopMember(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	member, ok := t.Members[name]
	if !ok {
		return
	}
	if member.Cancel != nil {
		member.Cancel()
	}
	member.Active = false
	t.persist()
}

func (t *Team) GetTeammateProgress() []*TeammateProgress {
	t.mu.Lock()
	defer t.mu.Unlock()
	var result []*TeammateProgress
	for _, m := range t.Members {
		if m.Progress != nil {
			result = append(result, m.Progress)
		}
	}
	return result
}

func (t *Team) SendMessage(from, to, content string) {
	_ = t.MailBox.Send(to, FileMailMessage{
		From:      from,
		Text:      content,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// --- 落盘：config.json ---

// TeamFile 是团队配置在磁盘上的形态，落在 <teamsBaseDir>/<slug>/config.json。
// 内存里的 Member 挂着 model / cancel 函数，这些没法序列化，所以落盘的是
// 另一份纯元信息的结构，两边靠成员名字对应。
type TeamFile struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	CreatedAt   int64            `json:"createdAt"`
	LeadAgentID string           `json:"leadAgentId"`
	Members     []TeamMemberFile `json:"members"`
}

type TeamMemberFile struct {
	AgentID      string `json:"agentId"`
	Name         string `json:"name"`
	AgentType    string `json:"agentType,omitempty"`
	Model        string `json:"model,omitempty"`
	JoinedAt     int64  `json:"joinedAt"`
	WorktreePath string `json:"worktreePath,omitempty"`
	BackendType  string `json:"backendType,omitempty"`
	IsActive     *bool  `json:"isActive,omitempty"`
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// sanitizeTeamName 把名字压成可以直接当文件名的形式。团队名/成员名是 LLM
// 起的，可能带空格、中文和标点。
func sanitizeTeamName(name string) string {
	return strings.ToLower(nonAlnum.ReplaceAllString(name, "-"))
}

func teamDir(name string) string {
	return filepath.Join(teamsBaseDir(), sanitizeTeamName(name))
}

func teamFilePath(name string) string {
	return filepath.Join(teamDir(name), "config.json")
}

// ReadTeamFile 读取团队配置。文件不存在返回 (nil, nil)。
func ReadTeamFile(name string) (*TeamFile, error) {
	data, err := os.ReadFile(teamFilePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tf TeamFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, err
	}
	return &tf, nil
}

// WriteTeamFile 写入团队配置，目录不存在会一并创建。
func WriteTeamFile(name string, tf *TeamFile) error {
	if err := os.MkdirAll(teamDir(name), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(teamFilePath(name), data, 0o644)
}

// snapshot 把内存里的 Team 导出成可落盘的 TeamFile。调用方持有 t.mu。
func (t *Team) snapshot() *TeamFile {
	tf := &TeamFile{
		Name:        t.Name,
		Description: t.Description,
		CreatedAt:   t.CreatedAt,
		LeadAgentID: t.LeadAgentID,
		Members:     make([]TeamMemberFile, 0, len(t.Members)),
	}
	if tf.CreatedAt == 0 {
		tf.CreatedAt = time.Now().Unix()
	}
	for _, m := range t.Members {
		active := m.Active
		tf.Members = append(tf.Members, TeamMemberFile{
			AgentID:      m.AgentID,
			Name:         m.Name,
			AgentType:    m.AgentType,
			Model:        m.Model,
			JoinedAt:     m.JoinedAt,
			WorktreePath: m.WorktreePath,
			BackendType:  string(t.Mode),
			IsActive:     &active,
		})
	}
	return tf
}

// persist 把当前状态写回磁盘。写失败不影响内存里的团队继续工作，只吞错误。
// 调用方持有 t.mu。
func (t *Team) persist() {
	_ = WriteTeamFile(t.Name, t.snapshot())
}

// --- TeamManager ---

type TeamManager struct {
	mu       sync.Mutex
	teams    map[string]*Team
	taskList map[string]*todo.TaskList // 每团队一份共享任务板
	workDir  string                    // 共享任务板落盘位置：<workDir>/.kcode/tasks/
}

func NewTeamManager(workDir string) *TeamManager {
	return &TeamManager{
		teams:    make(map[string]*Team),
		taskList: make(map[string]*todo.TaskList),
		workDir:  workDir,
	}
}

func (tm *TeamManager) CreateTeam(name string, mode TeamMode) *Team {
	return tm.CreateTeamFull(name, mode, LeadName, "")
}

// CreateTeamFull 建团队并记下 lead 和描述，随后把配置写进 config.json。
func (tm *TeamManager) CreateTeamFull(name string, mode TeamMode, leadAgentID, description string) *Team {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	team := NewTeam(name, mode)
	team.LeadAgentID = leadAgentID
	team.Description = description
	tm.teams[name] = team
	team.persist()
	return team
}

// GetTaskList 返回团队的共享任务板；内存无缓存时按 listID 懒建
// （todo.Store 会在第一次读写时处理磁盘状态）。
func (tm *TeamManager) GetTaskList(teamName string) *todo.TaskList {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if list, ok := tm.taskList[teamName]; ok {
		return list
	}
	listID := "team-" + sanitizeTeamName(teamName)
	list := todo.NewTaskList(todo.NewStore(tm.workDir, listID))
	tm.taskList[teamName] = list
	return list
}

// GetTeam 先查内存，未命中再看磁盘上有没有 config.json。从磁盘重建出来的
// Team 只带元信息，成员的 Eino 运行时是空的，够 SendMessage 按名字投递和
// UI 展示用；要真正让某个成员跑起来得重新 spawn。
func (tm *TeamManager) GetTeam(name string) *Team {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if team, ok := tm.teams[name]; ok {
		return team
	}
	tf, err := ReadTeamFile(name)
	if err != nil || tf == nil {
		return nil
	}
	team := NewTeam(tf.Name, ModeInProcess)
	team.LeadAgentID = tf.LeadAgentID
	team.Description = tf.Description
	team.CreatedAt = tf.CreatedAt
	for _, m := range tf.Members {
		active := false
		if m.IsActive != nil {
			active = *m.IsActive
		}
		team.Members[m.Name] = &Member{
			Name:         m.Name,
			AgentID:      m.AgentID,
			AgentType:    m.AgentType,
			Model:        m.Model,
			WorktreePath: m.WorktreePath,
			JoinedAt:     m.JoinedAt,
			Active:       active,
		}
	}
	tm.teams[name] = team
	return team
}

// DeleteTeam 停掉所有成员、清任务板和团队目录。
func (tm *TeamManager) DeleteTeam(name string) {
	tm.mu.Lock()
	team := tm.teams[name]
	delete(tm.teams, name)
	list := tm.taskList[name]
	delete(tm.taskList, name)
	tm.mu.Unlock()

	if team != nil {
		for memberName := range team.Members {
			team.StopMember(memberName)
		}
	}
	if list != nil {
		list.Clear()
	}
	// 团队目录里是 config.json 和收件箱，团队没了就一起清掉，
	// 免得下次同名团队捞到上一次的残留。
	_ = os.RemoveAll(teamDir(name))
}

func (tm *TeamManager) ListTeams() []string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	var names []string
	for name := range tm.teams {
		names = append(names, name)
	}
	return names
}

func (tm *TeamManager) GetAllTeammateProgress() []*TeammateProgress {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	var result []*TeammateProgress
	for _, team := range tm.teams {
		result = append(result, team.GetTeammateProgress()...)
	}
	return result
}

// FindTeamOf 返回成员所在的团队；memberName 为 LeadName 时返回第一个团队
// （Lead 不注册为 Member，它只读自己的信箱）。
func (tm *TeamManager) FindTeamOf(memberName string) *Team {
	tm.mu.Lock()
	names := make([]string, 0, len(tm.teams))
	for name := range tm.teams {
		names = append(names, name)
	}
	tm.mu.Unlock()

	for _, name := range names {
		team := tm.GetTeam(name)
		if team == nil {
			continue
		}
		if _, ok := team.Members[memberName]; ok {
			return team
		}
	}
	if memberName == LeadName && len(names) > 0 {
		return tm.GetTeam(names[0])
	}
	return nil
}

func (tm *TeamManager) CloseAll() {
	tm.mu.Lock()
	teams := make([]*Team, 0, len(tm.teams))
	for name, team := range tm.teams {
		teams = append(teams, team)
		delete(tm.teams, name)
	}
	tm.mu.Unlock()
	for _, team := range teams {
		for memberName := range team.Members {
			team.StopMember(memberName)
		}
	}
}
