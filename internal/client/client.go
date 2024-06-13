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
	muServices        sync.Mutex
	controlChannelMap map[string]*ControlChannel

	cancelCtx context.Context
	cancel    context.CancelFunc

	svc_chan chan string
}

func NewClient(conf *config.ClientConfig) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		Config:            conf,
		muServices:        sync.Mutex{},
		controlChannelMap: make(map[string]*ControlChannel),
		cancelCtx:         ctx,
		cancel:            cancel,

		svc_chan: make(chan string, len(conf.Services)),
	}
}

func (client *Client) Run(sigChan chan os.Signal) {
	defer client.cancel()

	go func() {
		for {
			select {
			case <-client.cancelCtx.Done():
				return
			case sn := <-client.svc_chan:
				client.run_new_controlchannel(sn)
			}
		}
	}()

	for svcName := range client.Config.Services {
		client.svc_chan <- svcName
	}

label_for:
	for {
		select {
		case <-client.cancelCtx.Done():
			fmt.Println("client receive c.cancelCtx.Done()")
			break label_for

		case sig := <-sigChan:
			fmt.Println("client receive interrupt", sig)
			break label_for
		}
	}
	time.Sleep(10 * time.Millisecond)
	fmt.Println("client goto shutdown")
}

func (client *Client) run_new_controlchannel(svcName string) {

	svcConfig, ok := client.getSvcConfigByName(svcName)
	if !ok {
		fmt.Println("run_new_controlchannel unknown service name", svcName)
		return
	}

	// 因为这里是单线程，所以不用加锁
	findCC, ok := client.controlChannelMap[svcName]
	if ok {
		fmt.Println("run_new_controlchannel find old cc, call cc.Close()")
		findCC.Close()
		delete(client.controlChannelMap, svcName)
	}

	cc := NewControlChannel(client, svcName, client.Config, &svcConfig)
	client.controlChannelMap[svcName] = cc

	go cc.Run()
}

func (client *Client) getSvcConfigByName(svcName string) (config.ClientServiceConfig, bool) {
	// 因为有热加载，所以需要加锁
	client.muServices.Lock()
	defer client.muServices.Unlock()
	svcConfig, ok := client.Config.Services[svcName]
	return svcConfig, ok
}
