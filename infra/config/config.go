package config

import (
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/service"
	"sync"
)

var (
	config *Config
	once   sync.Once
)

type Config struct {
	service.ServiceConf
	State string
	DB    struct {
		DSN string
	}
	ASR struct {
		AppKey    string
		AccessKey string
	}
	Comment struct {
		Assistant string
		Template  string
		ApiKey    string
		BaseURL   string
	}
	Consumers int
	Expire    int
}

func GetConfig() *Config {
	once.Do(func() {
		c := new(Config)
		if err := conf.Load("etc/config.yaml", c); err != nil {
			panic("get config error:" + err.Error())
		}
		if err := c.SetUp(); err != nil {
			panic("get config error:" + err.Error())
		}
		config = c
	})
	return config
}
