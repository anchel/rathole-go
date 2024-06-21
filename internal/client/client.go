package client

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/anchel/rathole-go/internal/config"
)

type ContextKey string

type ComunicationItem struct {
	method  string
	payload string
}

type Client struct {
	Config            *config.ClientConfig
	mu                sync.Mutex
	controlChannelMap map[string]*ControlChannel

	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup

	svc_chan chan string
}

func NewClient(ctx context.Context, conf *config.ClientConfig) *Client {
	baseCtx := context.WithValue(ctx, ContextKey("remoteAddr"), conf.RemoteAddr)
	ctx, cancel := context.WithCancel(baseCtx)

	return &Client{
		Config:            conf,
		mu:                sync.Mutex{},
		controlChannelMap: make(map[string]*ControlChannel),
		ctx:               ctx,
		cancel:            cancel,

		svc_chan: make(chan string, len(conf.Services)),
	}
}

func (client *Client) Run(sigChan chan os.Signal, updater chan *config.Config) {
	defer client.cancel()

	go func() {
		for {
			select {
			case <-client.ctx.Done():
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
		case <-client.ctx.Done():
			fmt.Println("client receive c.ctx.Done()")
			break label_for

		case sig := <-sigChan:
			fmt.Println("client receive interrupt", sig)
			client.cancel()
			break label_for

		case conf, ok := <-updater:
			if ok {
				fmt.Println("client receive update")
				client.hotUpdate(conf)
			}
		}
	}

	client.wg.Wait()
	fmt.Println("client goto shutdown")
}

func (client *Client) hotUpdate(newConfig *config.Config) {
	clientConfig := newConfig.Client

	client.mu.Lock()
	defer client.mu.Unlock()

	newServices := make([]string, 0, 6)
	delServices := make([]string, 0, 6)

	for svcName := range clientConfig.Services {
		_, ok := client.Config.Services[svcName]
		if !ok {
			newServices = append(newServices, svcName)
		}
	}

	for svcName := range client.Config.Services {
		_, ok := clientConfig.Services[svcName]
		if !ok {
			delServices = append(delServices, svcName)
		}
	}

	fmt.Println("client hotUpdate newServices", newServices)
	fmt.Println("client hotUpdate delServices", delServices)

	for _, svcName := range delServices {
		findCC, ok := client.controlChannelMap[svcName]
		if ok {
			fmt.Println("client hotUpdate find old cc, call cc.Close()", svcName)
			findCC.Close()
			delete(client.controlChannelMap, svcName)
		} else {
			fmt.Println("client hotUpdate unknown svcName", svcName)
		}
	}

	client.Config.Services = clientConfig.Services // 后面创建新的cc，依赖这一步的赋值

	if len(newServices) > 0 {
		go func() {
			for _, svcName := range newServices {
				select {
				case <-client.ctx.Done():
					return
				case client.svc_chan <- svcName:
				}
			}
		}()
	}
}

func (client *Client) run_new_controlchannel(svcName string) {
	client.mu.Lock()
	defer client.mu.Unlock()

	svcConfig, ok := client.Config.Services[svcName]
	if !ok {
		fmt.Println("run_new_controlchannel unknown service name", svcName)
		return
	}

	findCC, ok := client.controlChannelMap[svcName]
	if ok {
		fmt.Println("run_new_controlchannel find old cc, call cc.Close()")
		findCC.Close()
		delete(client.controlChannelMap, svcName)
	}

	cc := NewControlChannel(&client.ctx, svcName, &svcConfig)
	client.controlChannelMap[svcName] = cc

	ciChan := make(chan *ComunicationItem)

	go func() {
		for {
			item, ok := <-ciChan
			if !ok {
				fmt.Println("ciChan closed")
				return
			}
			if item.method == "reconnect" {
				select {
				case <-client.ctx.Done():
					return
				case client.svc_chan <- item.payload:
					fmt.Println("reconnect were sended to channel")
				}
			}
		}
	}()

	client.wg.Add(1)
	go func() {
		defer client.wg.Done()
		defer close(ciChan)
		cc.Run(ciChan)
	}()
}

func (client *Client) getSvcConfigByName(svcName string) (config.ClientServiceConfig, bool) {
	svcConfig, ok := client.Config.Services[svcName]
	return svcConfig, ok
}
