package coze

import (
	"fmt"

	"github.com/go-resty/resty/v2"
	"github.com/panjf2000/ants/v2"
)

const (
	HostCozeCN  = "https://api.coze.cn"
	HostCozeCOM = "https://api.coze.com"

	defaultChatPoolSize = 10000
)

type BotConfig struct {
	Host  string
	Token string
	BotId string
}

type Bot struct {
	config *BotConfig
	client *resty.Client

	chatPool *ants.Pool
}

func NewBot(cfg *BotConfig) *Bot {
	host := HostCozeCN
	if cfg.Host != "" {
		host = cfg.Host
	}
	client := resty.New().SetBaseURL(host).SetAuthToken(cfg.Token)
	chatPool, _ := ants.NewPool(defaultChatPoolSize)
	return &Bot{config: cfg, client: client, chatPool: chatPool}
}

type ErrorWrap struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (err ErrorWrap) Error() string {
	return fmt.Sprintf("code: %d, msg: %s", err.Code, err.Msg)
}
