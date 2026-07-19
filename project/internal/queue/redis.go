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

type Redis struct{ client *redis.Client }

func New(client *redis.Client) *Redis { return &Redis{client: client} }

func (q *Redis) Enqueue(ctx context.Context, id uint64, at time.Time) error {
	return q.client.ZAdd(ctx, ReadyKey, &redis.Z{Score: float64(at.UnixMilli()), Member: strconv.FormatUint(id, 10)}).Err()
}

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
	id, err := strconv.ParseUint(values[0], 10, 64)
	return id, err == nil, err
}

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

func (q *Redis) Processing(ctx context.Context) ([]string, error) {
	return q.client.ZRange(ctx, ProcessingKey, 0, -1).Result()
}

func (q *Redis) Heartbeat(ctx context.Context, ttl time.Duration) error {
	return q.client.Set(ctx, WorkerHeartbeatKey, time.Now().UTC().Format(time.RFC3339Nano), ttl).Err()
}

func (q *Redis) WorkerAlive(ctx context.Context) (bool, error) {
	count, err := q.client.Exists(ctx, WorkerHeartbeatKey).Result()
	return count == 1, err
}

func (q *Redis) Client() *redis.Client { return q.client }
