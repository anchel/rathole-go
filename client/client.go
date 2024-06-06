package client

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"

	"github.com/anchel/rathole-go/config"
)

type Client struct {
	Config            *config.ClientConfig
	ControlChannelMap map[string]*ControlChannel
}

func NewClient(c *config.ClientConfig) *Client {
	return &Client{c, make(map[string]*ControlChannel)}
}

func (c *Client) Run() {
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	for svcName, svcConfig := range c.Config.Services {
		ctx := context.WithoutCancel(ctx)
		cc := NewControlChannel(svcName, c.Config, &svcConfig)
		c.ControlChannelMap[svcName] = cc
		wg.Add(1)
		go func() {
			defer wg.Done()
			cc.Run(ctx)
		}()
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	sig := <-ch
	fmt.Println("interrupt: ", sig)
	cancel()
	wg.Wait()
}

func (c *Client) handle_hot_reload() {
	// todo
}
