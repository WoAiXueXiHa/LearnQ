package domain

import (
	"errors"
	"time"
)

type TaskStatus string

const (
	// 任务状态机把“已写数据库但未投递”“已进入队列”“已被 Worker 租用”分开表达，
	// 使崩溃恢复可以根据持久化状态精确补偿，而不是猜测任务是否执行过。
	TaskPending    TaskStatus = "pending"
	TaskQueued     TaskStatus = "queued"
	TaskProcessing TaskStatus = "processing"
	TaskRetryWait  TaskStatus = "retry_wait"
	TaskSucceeded  TaskStatus = "succeeded"
	TaskDead       TaskStatus = "dead"
)

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
	ID          uint64     `json:"id" gorm:"primaryKey"`
	Kind        string     `json:"kind"`
	Status      TaskStatus `json:"status"`
	PayloadJSON string     `json:"-" gorm:"column:payload_json"`
	AttemptNo   int        `json:"attempt_no"`
	// ExecutionGeneration 在人工重试时递增，隔离同一任务的不同执行世代。
	ExecutionGeneration int `json:"execution_generation"`
	// LeaseToken 标识一次具体领取；所有完成/失败写入都必须携带它作为 fencing token。
	LeaseToken  string     `json:"-" gorm:"column:lease_token"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	AvailableAt time.Time  `json:"available_at"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

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
	ID          uint64 `gorm:"primaryKey"`
	AggregateID uint64
	EventType   string
	PayloadJSON string `gorm:"column:payload_json"`
	PublishedAt *time.Time
	CreatedAt   time.Time
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
