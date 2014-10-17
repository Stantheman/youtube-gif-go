package redisPool

import (
	"github.com/Stantheman/youtube-gif-go/config"
	"github.com/Stantheman/youtube-gif-go/logger"
	"github.com/garyburd/redigo/redis"
	"time"
)

var Pool = pool()

func pool() *redis.Pool {
	conf := config.Get()
	connect := conf.Redis.Ip + ":" + conf.Redis.Port
	logger.Get().Info("Getting redis pool")

	return &redis.Pool{
		MaxIdle:     3,
		IdleTimeout: 240 * time.Second,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", connect)
			if err != nil {
				return nil, err
			}
			return c, err
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}
}
