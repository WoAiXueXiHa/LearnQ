package domain

import (
	"errors"
	"time"
)

type TaskStatus string

// 任务状态机把“已写数据库但未投递”“已进入队列”“已被 Worker 租用”分开表达，
// 使崩溃恢复可以根据持久化状态精确补偿，而不是猜测任务是否执行过。
const (
	TaskPending    TaskStatus = "pending"
	TaskQueued     TaskStatus = "queued"
	TaskProcessing TaskStatus = "processing"
	TaskRetryWait  TaskStatus = "retry_wait"
	TaskSucceeded  TaskStatus = "succeeded"
	TaskDead       TaskStatus = "dead"
)

// transitions 是状态机的邻接表：值为该状态可达的目标状态集合，非法迁移在 Transition 中被拒绝。
var transitions = map[TaskStatus]map[TaskStatus]bool{
	TaskPending:    {TaskQueued: true},
	TaskQueued:     {TaskProcessing: true},
	TaskProcessing: {TaskSucceeded: true, TaskRetryWait: true, TaskDead: true},
	TaskRetryWait:  {TaskQueued: true},
	TaskDead:       {TaskPending: true},
}

// CanTransition 是任务状态机的唯一合法边集合；新增状态时需同时检查 Store 中的条件更新。
func CanTransition(from, to TaskStatus) bool { return transitions[from][to] }

// RetryDelay 采用有上限的退避。当前最多重试两次，避免永久故障持续占用模型和队列资源。
func RetryDelay(attempt int) (time.Duration, bool) {
	switch attempt {
	case 1:
		return 2 * time.Second, true
	case 2:
		return 4 * time.Second, true
	default:
		return 0, false
	}
}

type AITask struct {
	// AvailableAt 是任务最早可执行时间，Dispatcher 以它作为 Redis ready ZSet 的 score 实现延迟队列。
	ID          uint64     `json:"id" gorm:"primaryKey"`
	Kind        string     `json:"kind"`
	Status      TaskStatus `json:"status"`
	PayloadJSON string     `json:"-" gorm:"column:payload_json"`
	AttemptNo   int        `json:"attempt_no"`
	// 一个任务可能被人工重试多次，如果用 bool，第二次重试就无法区分是第一代还是第二代了
	// int 递增，每次重试都有唯一的编号
	ExecutionGeneration int `json:"execution_generation"`
	// LeaseToken 标识一次具体领取；所有完成/失败写入都必须携带它作为 fencing token。
	// 防止过期的 worker 覆盖有效结果
	// json:"-" 告诉 JSON 序列化器跳过这个字段，API 返回的 JSON 里不会出现它
	LeaseToken  string     `json:"-" gorm:"column:lease_token"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	AvailableAt time.Time  `json:"available_at"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Transition 按状态机迁移内存中的任务状态；非法迁移返回错误且不改变原状态。
func (t *AITask) Transition(to TaskStatus) error {
	if !CanTransition(t.Status, to) {
		return errors.New("invalid task state transition")
	}
	t.Status = to
	return nil
}

type StudyRecord struct {
	ID                 uint64        `json:"id" gorm:"primaryKey"`
	Title              string        `json:"title"`
	Summary            string        `json:"summary"`
	DurationMinute     int           `json:"duration_minutes"`
	ContentFingerprint string        `json:"-"`
	CreatedAt          time.Time     `json:"created_at"`
	Modules            []StudyModule `json:"modules,omitempty"`
}

type StudyModule struct {
	ID            uint64 `json:"id" gorm:"primaryKey"`
	StudyRecordID uint64 `json:"study_record_id"`
	Category      string `json:"category"`
	Content       string `json:"content"`
}

type OutboxEvent struct {
	// OutboxEvent 与业务对象同事务写入，把跨 MySQL/Redis 的原子性问题转化为可重放投递。
	ID            uint64     `gorm:"primaryKey"`
	AggregateID   uint64     // 对应的具体 task.ID
	EventType     string     // 发生了什么事件
	PayloadJSON   string     `gorm:"column:payload_json"` // 事件携带的数据
	PublishedAt   *time.Time // nil 说明未处理，!nil 说明已经发布或安全跳过
	DispatchError string     // 非空表示聚合已终态/删除，事件被安全跳过
	CreatedAt     time.Time
}

type TaskAttempt struct {
	// TaskAttempt 保存每次租约执行的审计记录；Task 是当前快照，Attempt 是不可丢失的历史。
	ID                  uint64 `gorm:"primaryKey"`
	TaskID              uint64
	ExecutionGeneration int
	AttemptNo           int
	LeaseToken          string
	Status              string
	ErrorMessage        string
	StartedAt           time.Time
	FinishedAt          *time.Time
}

type Document struct {
	ID             uint64    `json:"id" gorm:"primaryKey"`
	Filename       string    `json:"filename"`
	MediaType      string    `json:"media_type"`
	ContentHash    string    `json:"content_hash"`
	Content        string    `json:"-" gorm:"type:longtext"`
	Status         string    `json:"status"`
	IndexVersion   string    `json:"index_version"`
	IndexingTaskID uint64    `json:"indexing_task_id"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type DocumentChunk struct {
	// ID 由文档、块序号和内容哈希稳定生成，重复索引会覆盖同一点而不是制造副本。
	ID          string    `json:"id" gorm:"primaryKey;size:64"`
	DocumentID  uint64    `json:"document_id"`
	ChunkIndex  int       `json:"chunk_index"`
	Title       string    `json:"title"`
	StartLine   int       `json:"start_line"`
	EndLine     int       `json:"end_line"`
	Content     string    `json:"content"`
	ContentHash string    `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

type Image struct {
	ID                uint64    `json:"id" gorm:"primaryKey"`
	StudyRecordID     *uint64   `json:"study_record_id,omitempty"`
	OriginalFilename  string    `json:"original_filename"`
	MediaType         string    `json:"media_type"`
	SizeBytes         int64     `json:"size_bytes"`
	Width             int       `json:"width"`
	Height            int       `json:"height"`
	ContentHash       string    `json:"-"`
	StoragePath       string    `json:"-"`
	Prompt            string    `json:"-"`
	Status            string    `json:"status"`
	DescriptionJSON   string    `json:"-" gorm:"column:description_json"`
	DescriptionModel  string    `json:"-"`
	DescriptionTaskID uint64    `json:"task_id"`
	DerivedDocumentID *uint64   `json:"derived_document_id"`
	LastError         string    `json:"last_error"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
