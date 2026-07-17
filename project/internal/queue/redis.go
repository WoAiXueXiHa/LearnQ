package queue

import (
	"context"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
)

const (
	ReadyKey      = "learnq:tasks:ready"
	ProcessingKey = "learnq:tasks:processing"
	LeaseTokenKey = "learnq:tasks:lease_tokens"
)

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
	key := strconv.FormatUint(id, 10)
	current, err := q.client.HGet(ctx, LeaseTokenKey, key).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	if current != token {
		return nil
	}
	pipe := q.client.TxPipeline()
	pipe.ZRem(ctx, ProcessingKey, key)
	pipe.HDel(ctx, LeaseTokenKey, key)
	_, err = pipe.Exec(ctx)
	return err
}

func (q *Redis) Expired(ctx context.Context) ([]uint64, error) {
	now, err := q.client.Time(ctx).Result()
	if err != nil {
		return nil, err
	}
	values, err := q.client.ZRangeByScore(ctx, ProcessingKey, &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(now.UnixMilli(), 10)}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if id, parseErr := strconv.ParseUint(value, 10, 64); parseErr == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

func (q *Redis) Client() *redis.Client { return q.client }
