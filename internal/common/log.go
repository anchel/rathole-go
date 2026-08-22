package common

import (
	"fmt"
	"os"
	"strconv"
)

// udp 转发路径上每个数据包都打日志会成为吞吐瓶颈（os.Stdout 的写入是串行加锁的），
// 而且 stdout 被重定向到无人读取的管道时会把整个进程阻塞住。
// 所以数据包级别的日志默认关闭，排障时用 RATHOLE_DEBUG=1 打开。
var debugEnabled = func() bool {
	b, err := strconv.ParseBool(os.Getenv("RATHOLE_DEBUG"))
	if err != nil {
		return false
	}
	return b
}()

func DebugEnabled() bool {
	return debugEnabled
}

func Debug(a ...any) {
	if debugEnabled {
		fmt.Println(a...)
	}
}
