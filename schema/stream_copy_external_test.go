/*
 * Copyright 2024 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package schema

import (
	"fmt"
	"io"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStream1(t *testing.T) {
	runtime.GOMAXPROCS(1)

	sr, sw := Pipe[int](0)
	go func() {
		for i := 0; i < 100; i++ {
			sw.Send(i, nil)
			time.Sleep(3 * time.Millisecond)
		}
		sw.Close()
	}()
	copied := sr.Copy(2)
	var (
		now   = time.Now().UnixMilli()
		ts    = []int64{now, now}
		tsOld = []int64{now, now}
	)
	var count int32
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		i := 0
		s := copied[0]
		for {
			n, e := s.Recv()
			if e != nil {
				if e == io.EOF {
					break
				}
			}
			tsOld[0] = ts[0]
			ts[0] = time.Now().UnixMilli()
			interval := ts[0] - tsOld[0]
			if interval >= 6 {
				atomic.AddInt32(&count, 1)
			}
			assert.Equal(t, i, n)
			i++
		}
		wg.Done()
	}()
	go func() {
		i := 0
		s := copied[1]
		for {
			n, e := s.Recv()
			if e != nil {
				if e == io.EOF {
					break
				}
			}
			tsOld[1] = ts[1]
			ts[1] = time.Now().UnixMilli()
			interval := ts[1] - tsOld[1]
			if interval >= 6 {
				atomic.AddInt32(&count, 1)
			}
			assert.Equal(t, i, n)
			i++
		}
		wg.Done()
	}()
	wg.Wait()
	t.Logf("count= %d", count)
}

type info struct {
	idx     int
	ts      int64
	after   int64
	content string
}

func TestCopyDelay(t *testing.T) {
	runtime.GOMAXPROCS(10)
	n := 3
	//m := 100
	s := newStream[string](0)
	scp := s.asReader().Copy(n)
	go func() {
		s.send("1", nil)
		s.send("2", nil)
		time.Sleep(time.Second)
		s.send("3", nil)
		s.closeSend()
	}()
	wg := sync.WaitGroup{}
	wg.Add(n)
	infoList := make([][]info, n)
	for i := 0; i < n; i++ {
		j := i
		go func() {
			defer func() {
				scp[j].Close()
				wg.Done()
			}()
			for {
				lastTime := time.Now()
				str, err := scp[j].Recv()
				if err == io.EOF {
					break
				}
				now := time.Now()
				infoList[j] = append(infoList[j], info{
					idx:     j,
					ts:      now.UnixMicro(),
					after:   now.Sub(lastTime).Milliseconds(),
					content: str,
				})
			}
		}()
	}
	wg.Wait()
	infos := make([]info, 0)
	for _, infoL := range infoList {
		for _, info := range infoL {
			infos = append(infos, info)
		}
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ts < infos[j].ts
	})
	for _, info := range infos {
		fmt.Printf("child[%d] ts[%d] after[%5dms] content[%s]\n", info.idx, info.ts, info.after, info.content)
	}
}
