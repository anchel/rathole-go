package common

import (
	"sync"
)

type Runnable interface {
	// Close 停止该对象，并且要等到它真正释放完资源（比如关掉监听端口）才返回，
	// 否则紧随其后启动的同名对象会因为端口还被占用而起不来。
	Close()
	Run()
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

// 空 key 一律不匹配，避免把不相干的条目扫出来。
func (rm *RunnableManager) find(key1 string, key2 string) (int, *RunnableItem) {
	for i, j := range rm.list {
		if (key1 != "" && j.key1 == key1) || (key2 != "" && j.key2 == key2) {
			return i, j
		}
	}
	return -1, nil
}

func (rm *RunnableManager) findItem(item Runnable) (int, *RunnableItem) {
	for i, j := range rm.list {
		if j.item == item {
			return i, j
		}
	}
	return -1, nil
}

// Put 用新对象替换掉同 key 的旧对象。
// 这里在持锁期间调用 Close 并等它结束：旧对象的清理逻辑（RemoveItem）会阻塞在锁上，
// 但 Close 只等 Run 返回、不等清理逻辑执行，所以不会死锁。
func (rm *RunnableManager) Put(key1 string, key2 string, item Runnable) {
	rm.Lock()
	defer rm.Unlock()
	idx, found := rm.find(key1, key2)
	if found != nil {
		found.item.Close()
		rm.remove(idx)
	}
	rm.list = append(rm.list, &RunnableItem{key1, key2, item})
}

func (rm *RunnableManager) GetByKey1(key1 string) Runnable {
	rm.Lock()
	defer rm.Unlock()
	_, found := rm.find(key1, "")
	if found == nil {
		return nil
	}
	return found.item
}

func (rm *RunnableManager) GetByKey2(key2 string) Runnable {
	rm.Lock()
	defer rm.Unlock()
	_, found := rm.find("", key2)
	if found == nil {
		return nil
	}
	return found.item
}

func (rm *RunnableManager) remove(index int) {
	if index < 0 || index > len(rm.list)-1 {
		return
	}
	rm.list = append(rm.list[:index], rm.list[index+1:]...)
}

// RemoveItem 按对象身份摘除条目。
// 收尾清理必须用这个而不是按 key 删：客户端重连时新通道会用同一个 key 注册进来，
// 旧通道退出时若按 key 删，删掉的正是刚建好的新通道。
func (rm *RunnableManager) RemoveItem(item Runnable) {
	rm.Lock()
	defer rm.Unlock()
	idx, found := rm.findItem(item)
	if found != nil {
		rm.remove(idx)
	}
}

func (rm *RunnableManager) RemoveByKey1(key1 string) {
	rm.Lock()
	defer rm.Unlock()
	idx, found := rm.find(key1, "")
	if found != nil {
		found.item.Close()
		rm.remove(idx)
	}
}

func (rm *RunnableManager) Len() int {
	rm.Lock()
	defer rm.Unlock()
	return len(rm.list)
}
