package callbacks

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type testCtxKey struct{}

type dummyHandler struct{}

func (dummyHandler) OnStart(ctx context.Context, _ *RunInfo, _ CallbackInput) context.Context     { return ctx }
func (dummyHandler) OnEnd(ctx context.Context, _ *RunInfo, _ CallbackOutput) context.Context       { return ctx }
func (dummyHandler) OnError(ctx context.Context, _ *RunInfo, _ error) context.Context              { return ctx }
func (dummyHandler) OnStartWithStreamInput(ctx context.Context, _ *RunInfo, _ *schema.StreamReader[CallbackInput]) context.Context {
	return ctx
}
func (dummyHandler) OnEndWithStreamOutput(ctx context.Context, _ *RunInfo, _ *schema.StreamReader[CallbackOutput]) context.Context {
	return ctx
}

// TestAppendAndOnRace 确定性复现：并发 On + AppendHandlers 时，On 对共享 handler
// backing array 的写入（append 到 cap 预留区）会与其他读取产生数据竞争。
//
// 复现要点：
//   - base manager 的 handlers slice 保持 cap > len（newManager 直接保存传入 slice），
//     使 On 内部的 append(nMgr.handlers, globalHandlers...) 写入共享 backing array 的
//     [len, cap) 预留区。
//   - 多个 goroutine 从同一个 base 并发执行 On 与 AppendHandlers，模拟并行图节点执行。
//   - handler 数量固定（不随迭代增长），避免 O(n²) 计算量导致测试卡死。
//
// 修复前（浅拷贝）：go test -race 报 "DATA RACE ... inject.go On ... AppendHandlers"。
// 修复后（深拷贝）：-race 干净通过。
func TestAppendAndOnRace(t *testing.T) {
	// 全局 handlers 非空，使 On 的 append(nMgr.handlers, globalHandlers...) 真正写入
	// 共享 backing array 的预留区，从而复现并发写。
	GlobalHandlers = []Handler{dummyHandler{}, dummyHandler{}}
	t.Cleanup(func() { GlobalHandlers = nil })

	// handlers 容量大于长度，保留 cap 预留区。
	hs := make([]Handler, 0, 16)
	for i := 0; i < 8; i++ {
		hs = append(hs, dummyHandler{})
	}
	base := InitCallbacks(context.Background(), &RunInfo{Name: "root"}, hs...)

	const goroutines = 4
	const iterations = 500

	var wg sync.WaitGroup
	var corrupted atomic.Int32

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				sid := fmt.Sprintf("session_%02d_%04d", gid, i)
				c := context.WithValue(base, testCtxKey{}, sid)

				// 并发：AppendHandlers 读取 handlers（复制），On 遍历 handlers（append 写预留区）。
				var wg2 sync.WaitGroup
				wg2.Add(2)

				go func() {
					defer wg2.Done()
					for j := 0; j < 8; j++ {
						_ = AppendHandlers(c, &RunInfo{Name: fmt.Sprintf("n%d", j)}, dummyHandler{})
					}
				}()

				go func() {
					defer wg2.Done()
					for j := 0; j < 8; j++ {
						_, _ = On(c, struct{}{}, OnStartHandle[struct{}], CallbackTiming(0), true)
					}
				}()

				wg2.Wait()

				retrieved, _ := c.Value(testCtxKey{}).(string)
				if retrieved != sid {
					corrupted.Add(1)
					t.Logf("CORRUPTED g=%d i=%d: %q -> %q (0x%02x->0x%02x)", gid, i, sid, retrieved, sid[0], retrieved[0])
					return
				}
			}
		}(g)
	}

	wg.Wait()
	if n := corrupted.Load(); n > 0 {
		t.Fatalf("%d corruptions in %d iterations", n, goroutines*iterations)
	}
	t.Logf("all %d clean", goroutines*iterations)
}
