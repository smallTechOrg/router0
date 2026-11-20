package db

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewClient creates a go-redis client from the given address, password, and database index.
func NewClient(addr, password string, db int) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
}

// Ping verifies the Redis connection is alive.
func Ping(ctx context.Context, client *redis.Client) error {
	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: ping: %w", err)
	}
	return nil
}
