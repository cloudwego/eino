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

		//buf := scp[0].csr.parent.mem.buf
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

		//assert.Equal(t, 0, buf.Len())
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
		//memo := copies[0].csr.parent.mem
		//assert.Equal(t, true, memo.hasFinished)
		//assert.Equal(t, 0, memo.buf.Len())
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
		wg.Wait()
		assert.Equal(t, m, int(copies[0].csr.parent.closedNum))
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
		assert.Equal(t, 0, int(copies[0].csr.parent.closedNum)) // not closed
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

// TestMergeNamedStreamReaders tests the functionality of MergeNamedStreamReaders
// with a focus on SourceEOF error handling.
func TestMergeNamedStreamReaders(t *testing.T) {
	t.Run("BasicSourceEOF", func(t *testing.T) {
		// Create two named streams
		sr1, sw1 := Pipe[string](2)
		sr2, sw2 := Pipe[string](2)

		// Merge the streams with names
		namedStreams := map[string]*StreamReader[string]{
			"stream1": sr1,
			"stream2": sr2,
		}
		mergedSR := MergeNamedStreamReaders(namedStreams)
		defer mergedSR.Close()

		// Send data to the first stream and close it immediately
		go func() {
			defer sw1.Close()
			sw1.Send("data1-1", nil)
			sw1.Send("data1-2", nil)
			// First stream ends
		}()

		// Send data to the second stream with a delay before closing
		go func() {
			defer sw2.Close()
			sw2.Send("data2-1", nil)
			sw2.Send("data2-2", nil)
			sw2.Send("data2-3", nil)
			// Second stream ends
		}()

		// Track received data and EOF sources
		receivedData := make(map[string][]string)
		eofSources := make([]string, 0, 2)

		for {
			chunk, err := mergedSR.Recv()
			if err != nil {
				// Check if it's a SourceEOF error
				if sourceName, ok := GetSourceName(err); ok {
					eofSources = append(eofSources, sourceName)
					t.Logf("Received EOF from source: %s", sourceName)
					continue // Continue receiving from other streams
				}

				// If it's a regular EOF, all streams have ended
				if errors.Is(err, io.EOF) {
					break
				}

				// Handle other errors
				t.Errorf("Error receiving data: %v", err)
				break
			}

			// Categorize data by prefix
			if len(chunk) >= 5 {
				prefix := chunk[:5]
				if prefix == "data1" {
					receivedData["stream1"] = append(receivedData["stream1"], chunk)
				} else if prefix == "data2" {
					receivedData["stream2"] = append(receivedData["stream2"], chunk)
				}
			}
		}

		// Verify we received both SourceEOF errors
		if len(eofSources) != 2 {
			t.Errorf("Expected 2 SourceEOF errors, got %d", len(eofSources))
		}

		// Verify the source names are correct
		expectedSources := map[string]bool{"stream1": false, "stream2": false}
		for _, source := range eofSources {
			if _, exists := expectedSources[source]; !exists {
				t.Errorf("Unexpected source name: %s", source)
			} else {
				expectedSources[source] = true
			}
		}

		// Verify all expected sources were seen
		for source, seen := range expectedSources {
			if !seen {
				t.Errorf("Did not receive SourceEOF for %s", source)
			}
		}

		// Verify we received all expected data
		if len(receivedData["stream1"]) != 2 {
			t.Errorf("Expected 2 items from stream1, got %d", len(receivedData["stream1"]))
		}

		if len(receivedData["stream2"]) != 3 {
			t.Errorf("Expected 3 items from stream2, got %d", len(receivedData["stream2"]))
		}
	})

	t.Run("EmptyStream", func(t *testing.T) {
		// Create two streams, one will be empty
		sr1, sw1 := Pipe[string](2)
		sr2, sw2 := Pipe[string](2)

		// Close the first stream immediately to make it empty
		sw1.Close()

		// Merge the streams with names
		namedStreams := map[string]*StreamReader[string]{
			"empty": sr1,
			"data":  sr2,
		}
		mergedSR := MergeNamedStreamReaders(namedStreams)
		defer mergedSR.Close()

		// Send data to the second stream
		go func() {
			defer sw2.Close()
			sw2.Send("test-data", nil)
		}()

		// Track received EOFs and data
		eofSources := make([]string, 0, 2)
		receivedData := make([]string, 0, 1)

		for {
			chunk, err := mergedSR.Recv()
			if err != nil {
				if sourceName, ok := GetSourceName(err); ok {
					eofSources = append(eofSources, sourceName)
					continue
				}

				if errors.Is(err, io.EOF) {
					break
				}

				t.Errorf("Error receiving data: %v", err)
				break
			}

			receivedData = append(receivedData, chunk)
		}

		// Verify we received EOF from the empty stream
		if len(eofSources) != 2 {
			t.Errorf("Expected 2 SourceEOF errors, got %d", len(eofSources))
		}

		// Verify the first EOF is from the empty stream
		if len(eofSources) > 0 && eofSources[0] != "empty" {
			t.Errorf("Expected first EOF from 'empty' stream, got '%s'", eofSources[0])
		}

		// Verify we received the data from the non-empty stream
		if len(receivedData) != 1 || receivedData[0] != "test-data" {
			t.Errorf("Expected to receive 'test-data', got %v", receivedData)
		}
	})

	t.Run("ArraySource", func(t *testing.T) {
		// Create three named streams
		sr1, sw1 := Pipe[string](2)
		sr2, sw2 := Pipe[string](2)
		sr3 := StreamReaderFromArray([]string{"data3-1", "data3-2", "data3-3"})

		// Merge the streams with names
		namedStreams := map[string]*StreamReader[string]{
			"stream1": sr1,
			"stream2": sr2,
			"stream3": sr3,
		}
		mergedSR := MergeNamedStreamReaders(namedStreams)
		defer mergedSR.Close()

		// Send data and close streams in sequence
		go func() {
			// First stream sends one item then closes
			sw1.Send("data1", nil)
			sw1.Close()

			// Second stream sends two items then closes
			sw2.Send("data2-1", nil)
			sw2.Send("data2-2", nil)
			sw2.Close()
		}()

		// Track EOF order and data count
		eofOrder := make([]string, 0, 3)
		dataCount := 0

		for {
			_, err := mergedSR.Recv()
			if err != nil {
				if sourceName, ok := GetSourceName(err); ok {
					eofOrder = append(eofOrder, sourceName)
					continue
				}

				if errors.Is(err, io.EOF) {
					break
				}

				t.Errorf("Error receiving data: %v", err)
				break
			}

			dataCount++
		}

		// Verify EOF count
		if len(eofOrder) != 3 {
			t.Errorf("Expected 3 SourceEOF errors, got %d", len(eofOrder))
		}

		// Verify data count
		if dataCount != 6 {
			t.Errorf("Expected 6 data items, got %d", dataCount)
		}
	})

	t.Run("ErrorPropagation", func(t *testing.T) {
		// Create two streams
		sr1, sw1 := Pipe[string](2)
		sr2, sw2 := Pipe[string](2)

		// Merge the streams with names
		namedStreams := map[string]*StreamReader[string]{
			"normal": sr1,
			"error":  sr2,
		}
		mergedSR := MergeNamedStreamReaders(namedStreams)
		defer mergedSR.Close()

		testError := errors.New("test error")

		// Send normal data to first stream
		go func() {
			defer sw1.Close()
			sw1.Send("normal-data", nil)
		}()

		// Send error to second stream
		go func() {
			defer sw2.Close()
			sw2.Send("", testError)
		}()

		// Track received errors
		var receivedError error

		for {
			_, err := mergedSR.Recv()
			if err != nil {
				// Skip SourceEOF errors
				if _, ok := GetSourceName(err); ok {
					continue
				}

				if errors.Is(err, io.EOF) {
					break
				}

				// Store the first non-EOF error
				receivedError = err
				break
			}
		}

		// Verify we received the test error
		if receivedError == nil || receivedError.Error() != testError.Error() {
			t.Errorf("Expected error '%v', got '%v'", testError, receivedError)
		}
	})
}
