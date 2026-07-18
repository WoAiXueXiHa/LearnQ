//go:build integration

package queue_test

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/WoAiXueXiHa/LeranQ/project/internal/queue"
	"github.com/go-redis/redis/v8"
)

func testQueue(t *testing.T) *queue.Redis {
	t.Helper()
	addr := os.Getenv("LEARNQ_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("LEARNQ_TEST_REDIS_ADDR is required")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	client.FlushDB(context.Background())
	t.Cleanup(func() { client.FlushDB(context.Background()); client.Close() })
	return queue.New(client)
}

func TestLuaClaimsDueTaskOnceWithRedisTime(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()
	if err := q.Enqueue(ctx, 42, time.UnixMilli(0)); err != nil {
		t.Fatal(err)
	}
	var winners int
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, ok, err := q.Claim(ctx, time.Minute, "token-"+strconv.Itoa(i))
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			if ok {
				if id != 42 {
					t.Errorf("id=%d", id)
				}
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}

func TestFutureTaskIsNotClaimed(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()
	if err := q.Enqueue(ctx, 7, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := q.Claim(ctx, time.Minute, "token"); err != nil || ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
}

func TestWorkerHeartbeatExpires(t *testing.T) {
	q := testQueue(t)
	ctx := context.Background()
	alive, err := q.WorkerAlive(ctx)
	if err != nil || alive {
		t.Fatalf("initial alive=%v err=%v", alive, err)
	}
	if err := q.Heartbeat(ctx, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if alive, err = q.WorkerAlive(ctx); err != nil || !alive {
		t.Fatalf("heartbeat alive=%v err=%v", alive, err)
	}
	time.Sleep(80 * time.Millisecond)
	if alive, err = q.WorkerAlive(ctx); err != nil || alive {
		t.Fatalf("expired alive=%v err=%v", alive, err)
	}
}
