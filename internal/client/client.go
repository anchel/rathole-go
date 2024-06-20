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
	muServices        sync.Mutex
	controlChannelMap map[string]*ControlChannel

	cancelCtx context.Context
	cancel    context.CancelFunc

	wg sync.WaitGroup

	svc_chan chan string
}

func NewClient(conf *config.ClientConfig) *Client {
	baseCtx := context.WithValue(context.Background(), ContextKey("remoteAddr"), conf.RemoteAddr)
	ctx, cancel := context.WithCancel(baseCtx)

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
			client.cancel()
			break label_for
		}
	}

	client.wg.Wait()
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

	cc := NewControlChannel(client, svcName, &svcConfig)
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
				case <-client.cancelCtx.Done():
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
	// 因为有热加载，所以需要加锁
	client.muServices.Lock()
	defer client.muServices.Unlock()
	svcConfig, ok := client.Config.Services[svcName]
	return svcConfig, ok
}
