package apxtrace

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedisOptsFromEnv_DefaultsToRedisURL(t *testing.T) {
	t.Setenv("APX_TRACE_REDIS_URL", "redis://example.com:6380/2")
	opts, err := RedisOptsFromEnv()
	require.NoError(t, err)
	require.Equal(t, "example.com:6380", opts.Addr)
	require.Equal(t, 2, opts.DB)
}

func TestRedisOptsFromEnv_FallsBackToCaddyStorageRedis(t *testing.T) {
	os.Unsetenv("APX_TRACE_REDIS_URL")
	t.Setenv("REDIS_HOST", "r.local")
	t.Setenv("REDIS_PORT", "6390")
	t.Setenv("REDIS_DB", "5")
	opts, err := RedisOptsFromEnv()
	require.NoError(t, err)
	require.Equal(t, "r.local:6390", opts.Addr)
	require.Equal(t, 5, opts.DB)
}

func TestRedisOptsFromEnv_ReturnsErrorWhenUnset(t *testing.T) {
	os.Unsetenv("APX_TRACE_REDIS_URL")
	os.Unsetenv("REDIS_HOST")
	os.Unsetenv("REDIS_PORT")
	_, err := RedisOptsFromEnv()
	require.Error(t, err)
}

func TestRedisOptsFromEnv_ExtractsPasswordFromURL(t *testing.T) {
	t.Setenv("APX_TRACE_REDIS_URL", "redis://ignored_user:secretpass@example.com:6379/0")
	opts, err := RedisOptsFromEnv()
	require.NoError(t, err)
	require.Equal(t, "secretpass", opts.Password)
}

func TestRedisOptsFromEnv_RedissEnablesTLS(t *testing.T) {
	t.Setenv("APX_TRACE_REDIS_URL", "rediss://example.com:6379/0")
	opts, err := RedisOptsFromEnv()
	require.NoError(t, err)
	require.NotNil(t, opts.TLSConfig)
}

func TestRedisOptsFromEnv_RejectsUnknownScheme(t *testing.T) {
	t.Setenv("APX_TRACE_REDIS_URL", "http://example.com:6379/0")
	_, err := RedisOptsFromEnv()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported scheme")
}
