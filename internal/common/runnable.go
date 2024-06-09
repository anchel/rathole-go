package common

import (
	"fmt"
	"net"
	"sync"
	"time"
)

type Runnable interface {
	Close()
	Run(net.Conn)
}

type RunnableItem struct {
	key1 string
	key2 string
	item Runnable
}

type RunnableManager struct {
	sync.Mutex
	list []*RunnableItem
}

func NewRunnableManager() *RunnableManager {
	return &RunnableManager{
		list: make([]*RunnableItem, 0),
	}
}

func (rm *RunnableManager) get(key1 string, key2 string) (int, *RunnableItem) {
	var idx int
	var findItem *RunnableItem
	for i, j := range rm.list {
		if j.key1 == key1 || j.key2 == key2 {
			idx = i
			findItem = j
			break
		}
	}
	return idx, findItem
}

func (rm *RunnableManager) Put(key1 string, key2 string, item Runnable) {
	rm.Lock()
	defer rm.Unlock()
	idx, findItem := rm.get(key1, key2)
	if findItem != nil {
		findItem.item.Close()
		rm.remove(idx)
		time.Sleep(1 * time.Millisecond) // give time for controlchannel to finish, such as stop listening
	}
	rm.list = append(rm.list, &RunnableItem{key1, key2, item})
}

func (rm *RunnableManager) Get(key1 string, key2 string) Runnable {
	var r Runnable
	_, findItem := rm.get(key1, key2)
	if findItem != nil {
		r = findItem.item
	}
	return r
}

func (rm *RunnableManager) remove(index int) {
	len := len(rm.list)
	if index < 0 || index > (len-1) {
		fmt.Println("index invalid", index)
	}
	rm.list = rm.list[:index]
	if index < (len - 1) {
		rm.list = append(rm.list, rm.list[index+1:]...)
	}
}

func (rm *RunnableManager) Remove(index int) {
	rm.Lock()
	defer rm.Unlock()
	rm.remove(index)
}

func (rm *RunnableManager) Len() int {
	rm.Lock()
	defer rm.Unlock()
	return len(rm.list)
}
