package client

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/anchel/rathole-go/internal/config"
)

type Client struct {
	Config            *config.ClientConfig
	mu                sync.Mutex
	ControlChannelMap map[string]*ControlChannel

	cancelCtx context.Context
	cancel    context.CancelFunc

	svc_chan chan string
}

func NewClient(c *config.ClientConfig) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		Config:            c,
		mu:                sync.Mutex{},
		ControlChannelMap: make(map[string]*ControlChannel),
		cancelCtx:         ctx,
		cancel:            cancel,

		svc_chan: make(chan string, len(c.Services)),
	}
}

func (c *Client) Run(sigChan chan os.Signal) {

	go func() {
	label_for1:
		for {
			select {
			case <-c.cancelCtx.Done():
				break label_for1
			case sn := <-c.svc_chan:
				c.run_new_controlchannel(sn)
			}
		}
	}()

	for svcName := range c.Config.Services {
		c.svc_chan <- svcName
	}

label_for:
	for {
		select {
		case cc := <-sigChan:
			fmt.Println("client receive interrupt", cc)
			c.cancel()
		case <-c.cancelCtx.Done():
			fmt.Println("client receive cancel")
			break label_for
		}
	}
	time.Sleep(10 * time.Millisecond)
	fmt.Println("client goto shutdown")
}

func (c *Client) run_new_controlchannel(svcName string) {

	c.mu.Lock()
	defer c.mu.Unlock()

	svcConfig, ok := c.Config.Services[svcName]
	if !ok {
		fmt.Println("run_new_controlchannel unknown service name", svcName)
		return
	}

	findCC, ok := c.ControlChannelMap[svcName]
	if ok {
		fmt.Println("run_new_controlchannel find old cc, call cc.Close()")
		findCC.Close()
		delete(c.ControlChannelMap, svcName)
	}

	cc := NewControlChannel(c, svcName, c.Config, &svcConfig)
	c.ControlChannelMap[svcName] = cc

	go cc.Run()
}
