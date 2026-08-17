package metrics

import (
	"context"
	"strconv"
	"time"

	"github.com/go-redis/redis/v8"
)

const redisKey = "learnq:metrics:counters"

type Recorder struct {
	Client *redis.Client
}

func (r *Recorder) Add(ctx context.Context, name string, delta int64) {
	if r == nil || r.Client == nil {
		return
	}
	_ = r.Client.HIncrBy(ctx, redisKey, name, delta).Err()
}

func (r *Recorder) Inc(ctx context.Context, name string) {
	r.Add(ctx, name, 1)
}

func (r *Recorder) Observe(ctx context.Context, name string, duration time.Duration) {
	if r == nil || r.Client == nil {
		return
	}
	pipe := r.Client.Pipeline()
	pipe.HIncrBy(ctx, redisKey, name+"_count", 1)
	pipe.HIncrBy(ctx, redisKey, name+"_sum_ms", duration.Milliseconds())
	_, _ = pipe.Exec(ctx)
}

func (r *Recorder) Snapshot(ctx context.Context) (map[string]float64, error) {
	if r == nil || r.Client == nil {
		return map[string]float64{}, nil
	}
	values, err := r.Client.HGetAll(ctx, redisKey).Result()
	if err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(values))
	for name, raw := range values {
		value, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr == nil {
			result[name] = value
		}
	}
	return result, nil
}
