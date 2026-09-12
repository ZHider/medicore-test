package audit

import (
	"testing"
)

// Record 是每个 HTTP 请求都要走一次的路径（审计中间件在链尾无条件调用）。
//
// 这条基准守护的是一个**设计承诺**：Record 绝不阻塞调用方。
// 它只往带缓冲的 channel 里投递，满了就丢弃并计数。
// 如果哪天有人把 Record 改成"缓冲满时阻塞等待"，基准不会失败（它只是变慢），
// 但真正的护栏是 TestWriter_NeverBlocksWhenBufferFull —— 基准负责的是
// 让"性能退化"这件事**可见**。
//
// 关注的是 ns/op 的量级：应当在几十纳秒级（一次 channel 投递 + 一次结构体拷贝）。
// 若涨到微秒级，说明有人在请求路径上加了锁或分配。
func BenchmarkWriter_Record(b *testing.B) {
	store := &fakeStore{}
	w := NewWriter(store, nil)
	w.Start()
	defer w.Stop()

	entry := sampleEntry()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Record(entry)
	}
}

// 缓冲满、后台协程不消费时的丢弃路径。
//
// 这是背压最差的情况：每个请求都走到 default 分支并给计数器 +1。
// 它必须和正常路径一样快 —— 否则"数据库挂了"会直接变成"接口全变慢"。
func BenchmarkWriter_RecordBufferFull(b *testing.B) {
	store := &fakeStore{}
	w := NewWriter(store, nil)
	// 刻意不 Start：后台协程不消费，缓冲区必然填满

	for i := 0; i < BufferSize; i++ {
		w.Record(sampleEntry())
	}

	entry := sampleEntry()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Record(entry) // 全部走丢弃分支
	}
}

// 从路由模板推导资源与动作 —— 每个请求调用一次。
func BenchmarkDescribe(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Describe("/api/v1/appointments/:id/preconsult/submit", "POST")
	}
}

// 超长 UA 的截断路径（含 UTF-8 校验），比正常 UA 慢，但要确认没退化到微秒级。
func BenchmarkTruncate_Multibyte(b *testing.B) {
	s := "Mozilla/5.0 中文客户端 🎉 一些很长的后缀描述文本用来把长度撑过 255 字节的阈值"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = truncate(s, 255)
	}
}
