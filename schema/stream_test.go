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
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStream(t *testing.T) {
	s := newStream[int](0)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			closed := s.send(i, nil)
			if closed {
				break
			}
		}
		s.closeSend()
	}()

	i := 0
	for {
		i++
		if i > 5 {
			s.closeRecv()
			break
		}
		v, err := s.recv()
		if err != nil {
			assert.ErrorIs(t, err, io.EOF)
			break
		}
		t.Log(v)
	}

	wg.Wait()
}

func TestStreamCopy(t *testing.T) {
	s := newStream[string](10)
	srs := s.asReader().Copy(2)

	s.send("a", nil)
	s.send("b", nil)
	s.send("c", nil)
	s.closeSend()

	defer func() {
		for _, sr := range srs {
			sr.Close()
		}
	}()

	for {
		v, err := srs[0].Recv()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		t.Log("copy 01 recv", v)
	}

	for {
		v, err := srs[1].Recv()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		t.Log("copy 02 recv", v)
	}

	for {
		v, err := s.recv()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		t.Log("recv origin", v)
	}

	t.Log("done")
}

func TestNewStreamCopy(t *testing.T) {
	t.Run("test one index recv channel blocked while other indexes could recv", func(t *testing.T) {
		s := newStream[string](1)
		scp := s.asReader().Copy(2)

		var t1, t2 time.Time

		go func() {
			s.send("a", nil)
			t1 = time.Now()
			time.Sleep(time.Millisecond * 200)
			s.send("a", nil)
			s.closeSend()
		}()

		wg := sync.WaitGroup{}
		wg.Add(2)

		go func() {
			defer func() {
				scp[0].Close()
				wg.Done()
			}()

			for {
				str, err := scp[0].Recv()
				if err == io.EOF {
					break
				}

				assert.NoError(t, err)
				assert.Equal(t, str, "a")
			}
		}()

		go func() {
			defer func() {
				scp[1].Close()
				wg.Done()
			}()

			time.Sleep(time.Millisecond * 100)
			for {
				str, err := scp[1].Recv()
				if err == io.EOF {
					break
				}

				if t2.IsZero() {
					t2 = time.Now()
				}

				assert.NoError(t, err)
				assert.Equal(t, str, "a")
			}
		}()

		wg.Wait()

		assert.True(t, t2.Sub(t1) < time.Millisecond*200)
	})

	t.Run("test one index recv channel blocked and other index closed", func(t *testing.T) {
		s := newStream[string](1)
		scp := s.asReader().Copy(2)

		go func() {
			s.send("a", nil)
			time.Sleep(time.Millisecond * 200)
			s.send("a", nil)
			s.closeSend()
		}()

		wg := sync.WaitGroup{}
		wg.Add(2)

		buf := scp[0].csr.parent.mem.buf
		go func() {
			defer func() {
				scp[0].Close()
				wg.Done()
			}()

			for {
				str, err := scp[0].Recv()
				if err == io.EOF {
					break
				}

				assert.NoError(t, err)
				assert.Equal(t, str, "a")
			}
		}()

		go func() {
			time.Sleep(time.Millisecond * 100)
			scp[1].Close()
			scp[1].Close() // try close multiple times
			wg.Done()
		}()

		wg.Wait()

		assert.Equal(t, 0, buf.Len())
	})

	t.Run("test long time recv", func(t *testing.T) {
		s := newStream[int](2)
		n := 1000
		go func() {
			for i := 0; i < n; i++ {
				s.send(i, nil)
			}

			s.closeSend()
		}()

		m := 100
		wg := sync.WaitGroup{}
		wg.Add(m)
		copies := s.asReader().Copy(m)
		for i := 0; i < m; i++ {
			idx := i
			go func() {
				cp := copies[idx]
				l := 0
				defer func() {
					assert.Equal(t, 1000, l)
					cp.Close()
					wg.Done()
				}()

				for {
					exp, err := cp.Recv()
					if err == io.EOF {
						break
					}

					assert.NoError(t, err)
					assert.Equal(t, exp, l)
					l++
				}
			}()
		}

		wg.Wait()
		memo := copies[0].csr.parent.mem
		assert.Equal(t, true, memo.hasFinished)
		assert.Equal(t, 0, memo.buf.Len())
	})

	t.Run("test closes", func(t *testing.T) {
		s := newStream[int](20)
		n := 1000
		go func() {
			for i := 0; i < n; i++ {
				s.send(i, nil)
			}

			s.closeSend()
		}()

		m := 100
		wg := sync.WaitGroup{}
		wg.Add(m)

		wgEven := sync.WaitGroup{}
		wgEven.Add(m / 2)

		copies := s.asReader().Copy(m)
		for i := 0; i < m; i++ {
			idx := i
			go func() {
				cp := copies[idx]
				l := 0
				defer func() {
					cp.Close()
					wg.Done()
					if idx%2 == 0 {
						wgEven.Done()
					}
				}()

				for {
					if idx%2 == 0 && l == idx {
						break
					}

					exp, err := cp.Recv()
					if err == io.EOF {
						break
					}

					assert.NoError(t, err)
					assert.Equal(t, exp, l)
					l++
				}
			}()
		}

		wgEven.Wait()
		memo := copies[0].csr.parent.mem
		// memo.mu.Lock()
		// closedNum := memo.closedNum
		// memo.mu.Unlock()
		// assert.Equal(t, m/2, closedNum)

		wg.Wait()
		assert.Equal(t, m, memo.closedNum)
		assert.Equal(t, 0, memo.buf.Len())
	})

	t.Run("test reader do no close", func(t *testing.T) {
		s := newStream[int](20)
		n := 1000
		go func() {
			for i := 0; i < n; i++ {
				s.send(i, nil)
			}

			s.closeSend()
		}()

		m := 4
		wg := sync.WaitGroup{}
		wg.Add(m)

		copies := s.asReader().Copy(m)
		for i := 0; i < m; i++ {
			idx := i
			go func() {
				cp := copies[idx]
				l := 0
				defer func() {
					wg.Done()
				}()

				for {
					exp, err := cp.Recv()
					if err == io.EOF {
						break
					}

					assert.NoError(t, err)
					assert.Equal(t, exp, l)
					l++
				}
			}()
		}

		wg.Wait()
		memo := copies[0].csr.parent.mem
		assert.Equal(t, 0, memo.closedNum) // not closed
		assert.Equal(t, 0, memo.buf.Len()) // buff cleared
	})

}

func checkStream(s *StreamReader[int]) error {
	defer s.Close()

	for i := 0; i < 10; i++ {
		chunk, err := s.Recv()
		if err != nil {
			return err
		}
		if chunk != i {
			return fmt.Errorf("receive err, expected:%d, actual: %d", i, chunk)
		}
	}
	_, err := s.Recv()
	if err != io.EOF {
		return fmt.Errorf("close chan fail")
	}
	return nil
}

func testStreamN(cap, n int) error {
	s := newStream[int](cap)
	go func() {
		for i := 0; i < 10; i++ {
			s.send(i, nil)
		}
		s.closeSend()
	}()

	vs := s.asReader().Copy(n)
	err := checkStream(vs[0])
	if err != nil {
		return err
	}

	vs = vs[1].Copy(n)
	err = checkStream(vs[0])
	if err != nil {
		return err
	}
	vs = vs[1].Copy(n)
	err = checkStream(vs[0])
	if err != nil {
		return err
	}
	return nil
}

func TestCopy(t *testing.T) {
	for i := 0; i < 10; i++ {
		for j := 2; j < 10; j++ {
			err := testStreamN(i, j)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCopy5(t *testing.T) {
	s := newStream[int](0)
	go func() {
		for i := 0; i < 10; i++ {
			closed := s.send(i, nil)
			if closed {
				fmt.Printf("has closed")
			}
		}
		s.closeSend()
	}()
	vs := s.asReader().Copy(5)
	time.Sleep(time.Second)
	defer func() {
		for _, v := range vs {
			v.Close()
		}
	}()
	for i := 0; i < 10; i++ {
		chunk, err := vs[0].Recv()
		if err != nil {
			t.Fatal(err)
		}
		if chunk != i {
			t.Fatalf("receive err, expected:%d, actual: %d", i, chunk)
		}
	}
	_, err := vs[0].Recv()
	if err != io.EOF {
		t.Fatalf("copied stream reader cannot return EOF")
	}
	_, err = vs[0].Recv()
	if err != io.EOF {
		t.Fatalf("copied stream reader cannot return EOF repeatedly")
	}
}

func TestStreamReaderWithConvert(t *testing.T) {
	s := newStream[int](2)

	var cntA int
	var e error

	convA := func(src int) (int, error) {
		if src == 1 {
			return 0, fmt.Errorf("mock err")
		}

		return src, nil
	}

	sta := StreamReaderWithConvert[int, int](s.asReader(), convA)

	s.send(1, nil)
	s.send(2, nil)
	s.closeSend()

	defer sta.Close()

	for {
		item, err := sta.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}

			e = err
			continue
		}

		cntA += item
	}

	assert.NotNil(t, e)
	assert.Equal(t, cntA, 2)
}

func TestArrayStreamCombined(t *testing.T) {
	asr := &StreamReader[int]{
		typ: readerTypeArray,
		ar: &arrayReader[int]{
			arr:   []int{0, 1, 2},
			index: 0,
		},
	}

	s := newStream[int](3)
	for i := 3; i < 6; i++ {
		s.send(i, nil)
	}
	s.closeSend()

	nSR := MergeStreamReaders([]*StreamReader[int]{asr, s.asReader()})

	record := make([]bool, 6)
	for i := 0; i < 6; i++ {
		chunk, err := nSR.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if record[chunk] {
			t.Fatal("record duplicated")
		}
		record[chunk] = true
	}

	_, err := nSR.Recv()
	if err != io.EOF {
		t.Fatal("reader haven't finish correctly")
	}

	for i := range record {
		if !record[i] {
			t.Fatal("record missing")
		}
	}
}

func TestMultiStream(t *testing.T) {
	var sts []*stream[int]
	sum := 0
	for i := 0; i < 10; i++ {
		size := rand.Intn(10) + 1
		sum += size
		st := newStream[int](size)
		for j := 1; j <= size; j++ {
			st.send(j&0xffff+i<<16, nil)
		}
		st.closeSend()
		sts = append(sts, st)
	}
	mst := newMultiStreamReader(sts)
	receiveList := make([]int, 10)
	for i := 0; i < sum; i++ {
		chunk, err := mst.recv()
		if err != nil {
			t.Fatal(err)
		}
		if receiveList[chunk>>16] >= chunk&0xffff {
			t.Fatal("out of order")
		}
		receiveList[chunk>>16] = chunk & 0xffff
	}
	_, err := mst.recv()
	if err != io.EOF {
		t.Fatal("end stream haven't return EOF")
	}
}
