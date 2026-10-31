package route

import (
	"runtime"

	"github.com/metacubex/chi/render"
	"github.com/metacubex/http"
)

// memstatsHandler 返回 Go 运行时的内存/GC 快照, 单位一律字节(名字与字段一一对应)。
//
// 为什么要它: 现有 /memory 走的是 tunnel/statistic 的进程 RSS(oslimit 硬编码 0),
// 拿不到"堆里到底有多少活对象"。实测打包上传时两进程各冲到 1.5~1.7 GB 又自行回落,
// 只看 RSS 无法区分「堆真的很大」和「mmap/arena 没还」—— 这里给的是 HeapAlloc /
// HeapReleased / Sys, 能直接算出没还给 OS 的那部分。
//
// 注意: ReadMemStats 会 STW 几十微秒量级, 只适合人工/低频采样, 不要放进秒级采集循环。
func memstatsHandler(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	render.JSON(w, r, render.M{
		"alloc":          ms.Alloc,
		"total_alloc":    ms.TotalAlloc,
		"sys":            ms.Sys,
		"heap_alloc":     ms.HeapAlloc,
		"heap_inuse":     ms.HeapInuse,
		"heap_idle":      ms.HeapIdle,
		"heap_released":  ms.HeapReleased,
		"stack_inuse":    ms.StackInuse,
		"num_gc":         ms.NumGC,
		"pause_total_ns": ms.PauseTotalNs,
		"num_goroutine":  runtime.NumGoroutine(),
	})
}
