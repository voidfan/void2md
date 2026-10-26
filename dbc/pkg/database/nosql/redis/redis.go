package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-redis/redis/v8"

	"void2md/dbc/pkg/database/types"
)

// RedisClient represents a Redis database client
type RedisClient struct {
	client   *redis.Client
	host     string
	port     string
	password string
	version  string
}

// NewRedisClient creates a new Redis client
func NewRedisClient(config types.DatabaseConfig) (types.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", config.Host, config.Port),
		Password: config.Password,
		DB:       0, // use default DB
	})

	// Test the connection
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping Redis: %w", err)
	}

	// Get Redis version
	info := rdb.Info(ctx, "server")
	if info.Err() == nil {
		infoStr := info.Val()
		// Parse version from INFO output
		lines := strings.Split(infoStr, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "redis_version:") {
				version := strings.TrimPrefix(line, "redis_version:")
				version = strings.TrimSpace(version)
				return &RedisClient{
					client:   rdb,
					host:     config.Host,
					port:     config.Port,
					password: config.Password,
					version:  version,
				}, nil
			}
		}
	}

	return &RedisClient{
		client:   rdb,
		host:     config.Host,
		port:     config.Port,
		password: config.Password,
		version:  config.Version,
	}, nil
}

// GetType returns the database type
func (c *RedisClient) GetType() types.DatabaseType {
	return types.Redis
}

// GetVersion returns the database version
func (c *RedisClient) GetVersion() string {
	return c.version
}

// Ping tests the Redis connection
func (c *RedisClient) Ping() error {
	ctx := context.Background()
	return c.client.Ping(ctx).Err()
}

// Close closes the Redis connection
func (c *RedisClient) Close() error {
	ctx := context.Background()
	return c.client.Close()
}

// GetSchemaContext gets schema context for Redis (key patterns and types)
func (c *RedisClient) GetSchemaContext(tables string) (string, error) {
	ctx := context.Background()

	// Get all keys (or filtered by pattern if tables specified)
	var pattern string
	if tables != "" {
		pattern = tables
	} else {
		pattern = "*"
	}

	keys, err := c.client.Keys(ctx, pattern).Result()
	if err != nil {
		return "", fmt.Errorf("failed to get keys: %w", err)
	}

	if len(keys) == 0 {
		return "No keys found in Redis database", nil
	}

	// Get types for each key
	var context strings.Builder
	context.WriteString("Redis Key Types:\n")

	// Sample first 100 keys to avoid performance issues
	sampleSize := 100
	if len(keys) < sampleSize {
		sampleSize = len(keys)
	}

	for i := 0; i < sampleSize; i++ {
		keyType, err := c.client.Type(ctx, keys[i]).Result()
		if err != nil {
			keyType = "unknown"
		}
		context.WriteString(fmt.Sprintf("- %s: %s\n", keys[i], keyType))
	}

	if len(keys) > sampleSize {
		context.WriteString(fmt.Sprintf("... and %d more keys\n", len(keys)-sampleSize))
	}

	return context.String(), nil
}

// ExecuteOperation executes a Redis operation
func (c *RedisClient) ExecuteOperation(op types.Operation) (*types.ExecuteQueryResponse, error) {
	if op.Type != types.OpCommand {
		return &types.ExecuteQueryResponse{Error: "Redis only supports COMMAND operations"}, nil
	}

	ctx := context.Background()

	// Parse the command - assume it's a JSON array of command parts
	var cmdParts []string
	if err := json.Unmarshal([]byte(op.Statement), &cmdParts); err != nil {
		return &types.ExecuteQueryResponse{Error: "Invalid Redis command format. Expected JSON array of strings"}, nil
	}

	if len(cmdParts) == 0 {
		return &types.ExecuteQueryResponse{Error: "Empty Redis command"}, nil
	}

	// Execute the command
	result, err := c.client.Do(ctx, cmdParts...).Result()
	if err != nil {
		return &types.ExecuteQueryResponse{Error: err.Error()}, nil
	}

	// Format result as rows
	var rows [][]interface{}
	var columns []string

	switch v := result.(type) {
	case string:
		columns = []string{"result"}
		rows = [][]interface{}{{v}}
	case int64:
		columns = []string{"result"}
		rows = [][]interface{}{{v}}
	case []byte:
		columns = []string{"result"}
		rows = [][]interface{}{{string(v)}}
	case []interface{}:
		// Array result
		columns = []string{"item"}
		for _, item := range v {
			rows = append(rows, []interface{}{fmt.Sprintf("%v", item)})
		}
	default:
		columns = []string{"result"}
		rows = [][]interface{}{{fmt.Sprintf("%v", result)}}
	}

	return &types.ExecuteQueryResponse{
		Columns: columns,
		Rows:    rows,
	}, nil
}
