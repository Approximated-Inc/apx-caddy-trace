package apxtrace

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrRedisNotConfigured is returned when no Redis env vars are present.
var ErrRedisNotConfigured = errors.New("apx-caddy-trace: no Redis configuration found")

// RedisOptsFromEnv builds *redis.Options from environment variables.
// Precedence: APX_TRACE_REDIS_URL > REDIS_HOST/PORT/PASSWORD/DB.
func RedisOptsFromEnv() (*redis.Options, error) {
	if raw := os.Getenv("APX_TRACE_REDIS_URL"); raw != "" {
		return parseRedisURL(raw)
	}
	host := os.Getenv("REDIS_HOST")
	port := os.Getenv("REDIS_PORT")
	if host == "" || port == "" {
		return nil, ErrRedisNotConfigured
	}
	db := 0
	if raw := os.Getenv("REDIS_DB"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid REDIS_DB: %w", err)
		}
		db = n
	}
	return &redis.Options{
		Addr:         fmt.Sprintf("%s:%s", host, port),
		Password:     os.Getenv("REDIS_PASSWORD"),
		DB:           db,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		PoolSize:     10,
	}, nil
}

func parseRedisURL(raw string) (*redis.Options, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid APX_TRACE_REDIS_URL: %w", err)
	}
	db := 0
	if u.Path != "" && u.Path != "/" {
		n, err := strconv.Atoi(u.Path[1:])
		if err != nil {
			return nil, fmt.Errorf("invalid DB in APX_TRACE_REDIS_URL: %w", err)
		}
		db = n
	}
	password, _ := u.User.Password()
	return &redis.Options{
		Addr:         u.Host,
		Password:     password,
		DB:           db,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		PoolSize:     10,
	}, nil
}

// NewRedisClient constructs a client from env-provided options. Non-blocking:
// initial connection failures surface on the first operation.
func NewRedisClient() (*redis.Client, error) {
	opts, err := RedisOptsFromEnv()
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opts), nil
}

// Ping verifies connectivity with a bounded timeout.
func Ping(ctx context.Context, c *redis.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.Ping(ctx).Err()
}
