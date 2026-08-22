package client

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
)

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

	svc_chan chan svc_create
}

type svc_create struct {
	svcName string
	delay   time.Duration
}

func NewClient(parentCtx context.Context, conf *config.ClientConfig) *Client {
	baseCtx := context.WithValue(parentCtx, common.ContextKey("remoteAddr"), conf.RemoteAddr)
	ctx, cancel := context.WithCancel(baseCtx)

	return &Client{
		Config:            conf,
		mu:                sync.Mutex{},
		controlChannelMap: make(map[string]*ControlChannel),
		ctx:               ctx,
		cancel:            cancel,

		svc_chan: make(chan svc_create, len(conf.Services)),
	}
}

func (client *Client) Run(sigChan chan os.Signal, updater chan *config.Config) {
	defer client.cancel()

	go func() {
		for {
			select {
			case <-client.ctx.Done():
				return
			case sc := <-client.svc_chan:
				go func() {
					time.Sleep(sc.delay)
					client.run_new_controlchannel(sc.svcName)
				}()
			}
		}
	}()

	for svcName := range client.Config.Services {
		client.svc_chan <- svc_create{svcName: svcName, delay: 1 * time.Second}
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

	time.Sleep(1 * time.Second) // wait for all goroutines to finish
	fmt.Println("client goto shutdown")
}

func (client *Client) hotUpdate(newConfig *config.Config) {
	clientConfig := newConfig.Client

	client.mu.Lock()
	defer client.mu.Unlock()

	newServices := make([]string, 0, 6)
	delServices := make([]string, 0, 6)
	changedServices := make([]string, 0, 6)

	for svcName, newSvc := range clientConfig.Services {
		oldSvc, ok := client.Config.Services[svcName]
		if !ok {
			newServices = append(newServices, svcName)
		} else if oldSvc != newSvc {
			// 名字没变但 token/local_addr/type 改了。
			// ControlChannel 持有的是创建时那份配置的拷贝，不重建的话改动到重启才生效。
			changedServices = append(changedServices, svcName)
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
	fmt.Println("client hotUpdate changedServices", changedServices)

	for _, svcName := range delServices {
		findCC, ok := client.controlChannelMap[svcName]
		if ok {
			fmt.Println("client hotUpdate find cc, call cc.Close()", svcName)
			findCC.Close()
			delete(client.controlChannelMap, svcName)
		} else {
			fmt.Println("client hotUpdate unknown svcName", svcName)
		}
	}

	client.Config.Services = clientConfig.Services // 后面创建新的cc，依赖这一步的赋值

	// 配置变更的服务直接重建，run_new_controlchannel 会关掉同名的旧通道
	restartServices := append(newServices, changedServices...)
	if len(restartServices) > 0 {
		go func() {
			for _, svcName := range restartServices {
				select {
				case <-client.ctx.Done():
					return
				case client.svc_chan <- svc_create{svcName: svcName, delay: 1 * time.Second}:
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
				fmt.Println("ciChan is closed")
				return
			}
			if item.method == "reconnect" {
				select {
				case <-client.ctx.Done():
					return
				case client.svc_chan <- svc_create{svcName: item.payload, delay: 3 * time.Second}:
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

		client.mu.Lock()
		defer client.mu.Unlock()
		findCC, ok := client.controlChannelMap[svcName]
		if ok {
			fmt.Println("run_new_controlchannel find finished cc, remove it")
			findCC.Close()
			delete(client.controlChannelMap, svcName)
		}
	}()
}
