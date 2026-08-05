package queue

import (
	"context"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
)

const (
	// ready/processing 使用 ZSET：score 分别表示可执行时间和租约截止时间。
	// token 单独存 Hash，便于 Lua 在 ACK 时校验领取者身份。
	ReadyKey           = "learnq:tasks:ready"
	ProcessingKey      = "learnq:tasks:processing"
	LeaseTokenKey      = "learnq:tasks:lease_tokens"
	WorkerHeartbeatKey = "learnq:worker:heartbeat"
)

// ackScript：仅当 Redis 中登记的 token 与提交者一致时删除 processing 项；
// 不匹配返回 0，旧 Worker 无法误删后来者的新租约。
var ackScript = redis.NewScript(`
local member = KEYS[1]
local stored = redis.call("HGET", KEYS[2], member)
if stored == ARGV[1] then
	redis.call("ZREM", KEYS[3], member)
	redis.call("HDEL", KEYS[2], member)
	return 1
end
return 0
`)

// cleanupScript：对账清理用。token 匹配或 Redis 侧已无 token（__missing__）时才删除，
// 避免读取后任务恰好被重新领取而误删新租约。
var cleanupScript = redis.NewScript(`
local member = KEYS[1]
local stored = redis.call("HGET", KEYS[2], member)
if (ARGV[1] == "__missing__" and not stored) or stored == ARGV[1] then
	redis.call("ZREM", KEYS[3], member)
	redis.call("HDEL", KEYS[2], member)
	return 1
end
return 0
`)

// claimScript：原子完成领取——取最早到期任务、移出 ready、以“当前时间+租约时长”为 score
// 加入 processing，并登记 token。任一内部步骤失败都不会留下半领取状态。
var claimScript = redis.NewScript(`
local now = redis.call("TIME")
local nowms = now[1] * 1000 + math.floor(now[2] / 1000)
local items = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", nowms, "LIMIT", 0, 1)
if #items == 0 then return {} end
local id = items[1]
if redis.call("ZREM", KEYS[1], id) == 0 then return {} end
redis.call("ZADD", KEYS[2], nowms + tonumber(ARGV[1]), id)
redis.call("HSET", KEYS[3], id, ARGV[2])
return {id, tostring(nowms + tonumber(ARGV[1]))}
`)

// Redis 封装 redis.Client 与 ZSet/Lua 脚本，是任务队列的唯一访问入口。
type Redis struct{ client *redis.Client }

func New(client *redis.Client) *Redis { return &Redis{client: client} }

// Enqueue 按可执行时间把任务放入 ready ZSet；重复入队幂等（ZADD 覆盖同 score）。
func (q *Redis) Enqueue(ctx context.Context, id uint64, at time.Time) error {
	return q.client.ZAdd(ctx, ReadyKey, &redis.Z{Score: float64(at.UnixMilli()), Member: strconv.FormatUint(id, 10)}).Err()
}

// Claim 领取最早到期的任务并授予租约；无任务可领时返回 (0, false, nil)。
func (q *Redis) Claim(ctx context.Context, lease time.Duration, token string) (uint64, bool, error) {
	// 领取必须在 Redis 内原子完成“取到期任务、移出 ready、加入 processing、记录 token”。
	// 使用 Redis TIME 而不是各 Worker 本机时间，避免多机时钟偏差造成提前/延后领取。
	values, err := claimScript.Run(ctx, q.client, []string{ReadyKey, ProcessingKey, LeaseTokenKey}, lease.Milliseconds(), token).StringSlice()
	if err == redis.Nil || len(values) == 0 {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	// 解析失败时 ok=false 且返回 err，调用方按未领取处理。
	id, err := strconv.ParseUint(values[0], 10, 64)
	return id, err == nil, err
}

// Ack 释放租约：仅当 token 匹配时删除 processing 项；项已不存在同样视为成功（幂等）。
func (q *Redis) Ack(ctx context.Context, id uint64, token string) error {
	// ACK 只删除 token 仍匹配的 processing 项；旧 Worker 无法误删后来者的新租约。
	key := strconv.FormatUint(id, 10)
	_, err := ackScript.Run(ctx, q.client, []string{key, LeaseTokenKey, ProcessingKey}, token).Result()
	if err == redis.Nil {
		return nil
	}
	return err
}

// Cleanup 清理对账时观察到的幽灵 processing 项。先读 token，再由 Lua 比较并删除，
// 防止读取后任务恰好被重新领取，从而误删新 Worker 的租约。
func (q *Redis) Cleanup(ctx context.Context, id uint64) error {
	key := strconv.FormatUint(id, 10)
	token, err := q.client.HGet(ctx, LeaseTokenKey, key).Result()
	if err == redis.Nil {
		token = "__missing__"
	} else if err != nil {
		return err
	}
	_, err = cleanupScript.Run(ctx, q.client, []string{key, LeaseTokenKey, ProcessingKey}, token).Result()
	if err == redis.Nil {
		return nil
	}
	return err
}

// Processing 列出 processing ZSet 中的全部任务 ID，供 Reconciler 对账扫描使用。
func (q *Redis) Processing(ctx context.Context) ([]string, error) {
	return q.client.ZRange(ctx, ProcessingKey, 0, -1).Result()
}

// Heartbeat 定期写入带 TTL 的心跳键；Worker 停止后心跳过期，WorkerAlive 即判 Worker 死亡。
func (q *Redis) Heartbeat(ctx context.Context, ttl time.Duration) error {
	return q.client.Set(ctx, WorkerHeartbeatKey, time.Now().UTC().Format(time.RFC3339Nano), ttl).Err()
}

// WorkerAlive 通过心跳键是否存在判断 Worker 是否存活。
func (q *Redis) WorkerAlive(ctx context.Context) (bool, error) {
	count, err := q.client.Exists(ctx, WorkerHeartbeatKey).Result()
	return count == 1, err
}

// Client 暴露底层 redis.Client，供需要原生命令的调用方使用。
func (q *Redis) Client() *redis.Client { return q.client }
