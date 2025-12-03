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
	"reflect"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/cloudwego/eino/internal/safe"
)

// ErrNoValue is used during StreamReaderWithConvert to skip a streamItem, excluding it from the converted stream.
// e.g.
//
// outStream = schema.StreamReaderWithConvert(s,
//
//	func(src string) (string, error) {
//		if len(src) == 0 {
//			return nil, schema.ErrNoValue
//		}
//
//		return src.Message, nil
//	})
//
// outStream will filter out the empty string.
//
// DO NOT use it under other circumstances.
var ErrNoValue = errors.New("no value")

// ErrRecvAfterClosed indicates that StreamReader.Recv was unexpectedly called after StreamReader.Close.
// This error should not occur during normal use of StreamReader.Recv. If it does, please check your application code.
var ErrRecvAfterClosed = errors.New("recv after stream closed")

// SourceEOF represents an EOF error from a specific source stream.
// It is only returned by the method Recv() of StreamReader created
// with MergeNamedStreamReaders when one of its source streams reaches EOF.
type SourceEOF struct {
	sourceName string
}

func (e *SourceEOF) Error() string {
	return fmt.Sprintf("EOF from source stream: %s", e.sourceName)
}

// GetSourceName extracts the source stream name from a SourceEOF error.
// It returns the source name and a boolean indicating whether the error was a SourceEOF.
// If the error is not a SourceEOF, it returns an empty string and false.
func GetSourceName(err error) (string, bool) {
	var sErr *SourceEOF
	if errors.As(err, &sErr) {
		return sErr.sourceName, true
	}

	return "", false
}

// Pipe creates a new stream with the given capacity that represented with StreamWriter and StreamReader.
// The capacity is the maximum number of items that can be buffered in the stream.
// e.g.
//
//	sr, sw := schema.Pipe[string](3)
//	go func() { // send data
//		defer sw.Close()
//		for i := 0; i < 10; i++ {
//			sw.Send(i, nil)
//		}
//	}
//
//	defer sr.Close()
//	for chunk, err := sr.Recv() {
//		if errors.Is(err, io.EOF) {
//			break
//		}
//		fmt.Println(chunk)
//	}
func Pipe[T any](cap int) (*StreamReader[T], *StreamWriter[T]) {
	stm := newStream[T](cap)
	return stm.asReader(), &StreamWriter[T]{stm: stm}
}

// StreamWriter the sender of a stream.
// created by Pipe function.
// eg.
//
//	sr, sw := schema.Pipe[string](3)
//	go func() { // send data
//		defer sw.Close()
//		for i := 0; i < 10; i++ {
//			sw.Send(i, nil)
//		}
//	}
type StreamWriter[T any] struct {
	stm *stream[T]
}

// Send sends a value to the stream.
// e.g.
//
//	closed := sw.Send(i, nil)
//	if closed {
//		// the stream is closed
//	}
func (sw *StreamWriter[T]) Send(chunk T, err error) (closed bool) {
	return sw.stm.send(chunk, err)
}

// Close notify the receiver that the stream sender has finished.
// The stream receiver will get an error of io.EOF from StreamReader.Recv().
// Notice: always remember to call Close() after sending all data.
// eg.
//
//	defer sw.Close()
//	for i := 0; i < 10; i++ {
//		sw.Send(i, nil)
//	}
func (sw *StreamWriter[T]) Close() {
	sw.stm.closeSend()
}

// StreamReader the receiver of a stream.
// created by Pipe function.
// eg.
//
//	sr, sw := schema.Pipe[string](3)
//	// omit sending data
//	// most of time, reader is returned by function, and used in another function.
//
//	for chunk, err := sr.Recv() {
//		if errors.Is(err, io.EOF) {
//			break
//		}
//		if err != nil {
//			// handle error
//		}
//		fmt.Println(chunk)
//	}
type StreamReader[T any] struct {
	typ readerType

	st *stream[T]

	ar *arrayReader[T]

	msr *multiStreamReader[T]

	srw *streamReaderWithConvert[T]

	csr *childStreamReader[T]
}

// Recv receives a value from the stream.
// eg.
//
//	for chunk, err := sr.Recv() {
//		if errors.Is(err, io.EOF) {
//			break
//		}
//		if err != nil {
//		fmt.Println(chunk)
//	}
func (sr *StreamReader[T]) Recv() (T, error) {
	switch sr.typ {
	case readerTypeStream:
		return sr.st.recv()
	case readerTypeArray:
		return sr.ar.recv()
	case readerTypeMultiStream:
		return sr.msr.recv()
	case readerTypeWithConvert:
		return sr.srw.recv()
	case readerTypeChild:
		return sr.csr.recv()
	default:
		panic("impossible")
	}
}

// Close safely closes the StreamReader.
// It should be called only once, as multiple calls may not work as expected.
// Notice: always remember to call Close() after using Recv().
// e.g.
//
//	defer sr.Close()
//
//	for chunk, err := sr.Recv() {
//		if errors.Is(err, io.EOF) {
//			break
//		}
//		fmt.Println(chunk)
//	}
func (sr *StreamReader[T]) Close() {
	switch sr.typ {
	case readerTypeStream:
		sr.st.closeRecv()
	case readerTypeArray:

	case readerTypeMultiStream:
		sr.msr.close()
	case readerTypeWithConvert:
		sr.srw.close()
	case readerTypeChild:
		sr.csr.close()
	default:
		panic("impossible")
	}
}

// Copy creates a slice of new StreamReader.
// The number of copies, indicated by the parameter n, should be a non-zero positive integer.
// The original StreamReader will become unusable after Copy.
// eg.
//
//	sr := schema.StreamReaderFromArray([]int{1, 2, 3})
//	srs := sr.Copy(2)
//
//	sr1 := srs[0]
//	sr2 := srs[1]
//	defer sr1.Close()
//	defer sr2.Close()
//
//	chunk1, err1 := sr1.Recv()
//	chunk2, err2 := sr2.Recv()
func (sr *StreamReader[T]) Copy(n int) []*StreamReader[T] {
	if n < 2 {
		return []*StreamReader[T]{sr}
	}

	if sr.typ == readerTypeArray {
		ret := make([]*StreamReader[T], n)
		for i, ar := range sr.ar.copy(n) {
			ret[i] = &StreamReader[T]{typ: readerTypeArray, ar: ar}
		}
		return ret
	}

	return copyStreamReaders[T](sr, n)
}

// SetAutomaticClose sets the StreamReader to automatically close when it's no longer reachable and ready to be GCed.
// NOT concurrency safe.
func (sr *StreamReader[T]) SetAutomaticClose() {
	switch sr.typ {
	case readerTypeStream:
		if !sr.st.automaticClose {
			sr.st.automaticClose = true
			var flag uint32
			sr.st.closedFlag = &flag
			runtime.SetFinalizer(sr, func(s *StreamReader[T]) {
				s.Close()
			})
		}
	case readerTypeMultiStream:
		for _, s := range sr.msr.nonClosedStreams() {
			if !s.automaticClose {
				s.automaticClose = true
				var flag uint32
				s.closedFlag = &flag
				runtime.SetFinalizer(s, func(st *stream[T]) {
					st.closeRecv()
				})
			}
		}
	case readerTypeChild:
		parent := sr.csr.parent.sr
		parent.SetAutomaticClose()
	case readerTypeWithConvert:
		sr.srw.sr.SetAutomaticClose()
	case readerTypeArray:
		// no need to clean up
	default:
	}
}

func (sr *StreamReader[T]) recvAny() (any, error) {
	return sr.Recv()
}

func (sr *StreamReader[T]) copyAny(n int) []iStreamReader {
	ret := make([]iStreamReader, n)

	srs := sr.Copy(n)

	for i := 0; i < n; i++ {
		ret[i] = srs[i]
	}

	return ret
}

func arrToStream[T any](arr []T) *stream[T] {
	s := newStream[T](len(arr))
	for i := range arr {
		s.send(arr[i], nil)
	}
	s.closeSend()

	return s
}

func (sr *StreamReader[T]) toStream() *stream[T] {
	switch sr.typ {
	case readerTypeStream:
		return sr.st
	case readerTypeArray:
		return sr.ar.toStream()
	case readerTypeMultiStream:
		return sr.msr.toStream()
	case readerTypeWithConvert:
		return sr.srw.toStream()
	case readerTypeChild:
		return sr.csr.toStream()
	default:
		panic("impossible")
	}
}

type readerType int

const (
	readerTypeStream readerType = iota
	readerTypeArray
	readerTypeMultiStream
	readerTypeWithConvert
	readerTypeChild
)

type iStreamReader interface {
	recvAny() (any, error)
	copyAny(int) []iStreamReader
	Close()
	SetAutomaticClose()
}

// stream is a channel-based stream with 1 sender and 1 receiver.
// The sender calls closeSend() to notify the receiver that the stream sender has finished.
// The receiver calls closeRecv() to notify the sender that the receiver stop receiving.
type stream[T any] struct {
	items chan streamItem[T]

	closed chan struct{}

	automaticClose bool
	closedFlag     *uint32 // 0 = not closed, 1 = closed, only used when automaticClose is set
}

type streamItem[T any] struct {
	chunk T
	err   error
}

func newStream[T any](cap int) *stream[T] {
	return &stream[T]{
		items:  make(chan streamItem[T], cap),
		closed: make(chan struct{}),
	}
}

func (s *stream[T]) asReader() *StreamReader[T] {
	return &StreamReader[T]{typ: readerTypeStream, st: s}
}

func (s *stream[T]) recv() (chunk T, err error) {
	item, ok := <-s.items

	if !ok {
		item.err = io.EOF
	}

	return item.chunk, item.err
}

func (s *stream[T]) send(chunk T, err error) (closed bool) {
	// if the stream is closed, return immediately
	select {
	case <-s.closed:
		return true
	default:
	}

	item := streamItem[T]{chunk, err}

	select {
	case <-s.closed:
		return true
	case s.items <- item:
		return false
	}
}

func (s *stream[T]) closeSend() {
	close(s.items)
}

func (s *stream[T]) closeRecv() {
	if s.automaticClose {
		if atomic.CompareAndSwapUint32(s.closedFlag, 0, 1) {
			close(s.closed)
		}
		return
	}

	close(s.closed)
}

// StreamReaderFromArray creates a StreamReader from a given slice of elements.
// It takes an array of type T and returns a pointer to a StreamReader[T].
// This allows for streaming the elements of the array in a controlled manner.
// eg.
//
//	sr := schema.StreamReaderFromArray([]int{1, 2, 3})
//	defer sr.Close()
//
//	for chunk, err := sr.Recv() {
//		fmt.Println(chunk)
//	}
func StreamReaderFromArray[T any](arr []T) *StreamReader[T] {
	return &StreamReader[T]{ar: &arrayReader[T]{arr: arr}, typ: readerTypeArray}
}

type arrayReader[T any] struct {
	arr   []T
	index int
}

func (ar *arrayReader[T]) recv() (T, error) {
	if ar.index < len(ar.arr) {
		ret := ar.arr[ar.index]
		ar.index++

		return ret, nil
	}

	var t T
	return t, io.EOF
}

func (ar *arrayReader[T]) copy(n int) []*arrayReader[T] {
	ret := make([]*arrayReader[T], n)

	for i := 0; i < n; i++ {
		ret[i] = &arrayReader[T]{
			arr:   ar.arr,
			index: ar.index,
		}
	}

	return ret
}

func (ar *arrayReader[T]) toStream() *stream[T] {
	return arrToStream(ar.arr[ar.index:])
}

type multiArrayReader[T any] struct {
	ars   []*arrayReader[T]
	index int
}

type multiStreamReader[T any] struct {
	sts []*stream[T]

	itemsCases []reflect.SelectCase

	nonClosed []int

	sourceReaderNames []string
}

func newMultiStreamReader[T any](sts []*stream[T]) *multiStreamReader[T] {
	var itemsCases []reflect.SelectCase
	if len(sts) > maxSelectNum {
		itemsCases = make([]reflect.SelectCase, len(sts))
		for i, st := range sts {
			itemsCases[i] = reflect.SelectCase{
				Dir:  reflect.SelectRecv,
				Chan: reflect.ValueOf(st.items),
			}
		}
	}

	nonClosed := make([]int, len(sts))
	for i := range sts {
		nonClosed[i] = i
	}

	return &multiStreamReader[T]{
		sts:        sts,
		itemsCases: itemsCases,
		nonClosed:  nonClosed,
	}
}

func (msr *multiStreamReader[T]) recv() (T, error) {
	for len(msr.nonClosed) > 0 {
		var chosen int
		var ok bool
		if len(msr.nonClosed) > maxSelectNum {
			var recv reflect.Value
			chosen, recv, ok = reflect.Select(msr.itemsCases)
			if ok {
				item := recv.Interface().(streamItem[T])
				return item.chunk, item.err
			}
			msr.itemsCases[chosen].Chan = reflect.Value{}
		} else {
			var item *streamItem[T]
			chosen, item, ok = receiveN(msr.nonClosed, msr.sts)
			if ok {
				return item.chunk, item.err
			}
		}

		// delete the closed stream
		for i := range msr.nonClosed {
			if msr.nonClosed[i] == chosen {
				msr.nonClosed = append(msr.nonClosed[:i], msr.nonClosed[i+1:]...)
				break
			}
		}

		if len(msr.sourceReaderNames) > 0 {
			var t T
			return t, &SourceEOF{msr.sourceReaderNames[chosen]}
		}
	}

	var t T
	return t, io.EOF
}

func (msr *multiStreamReader[T]) nonClosedStreams() []*stream[T] {
	ret := make([]*stream[T], len(msr.nonClosed))

	for i, idx := range msr.nonClosed {
		ret[i] = msr.sts[idx]
	}

	return ret
}

func (msr *multiStreamReader[T]) close() {
	for _, s := range msr.sts {
		s.closeRecv()
	}
}

func (msr *multiStreamReader[T]) toStream() *stream[T] {
	return toStream[T, *multiStreamReader[T]](msr)
}

type streamReaderWithConvert[T any] struct {
	sr iStreamReader

	convert func(any) (T, error)

	errWrapper func(error) error
}

func newStreamReaderWithConvert[T any](origin iStreamReader, convert func(any) (T, error), opts ...ConvertOption) *StreamReader[T] {
	opt := &convertOptions{}
	for _, o := range opts {
		o(opt)
	}

	srw := &streamReaderWithConvert[T]{
		sr:         origin,
		convert:    convert,
		errWrapper: opt.ErrWrapper,
	}

	return &StreamReader[T]{
		typ: readerTypeWithConvert,
		srw: srw,
	}
}

type convertOptions struct {
	ErrWrapper func(error) error
}

type ConvertOption func(*convertOptions)

// WithErrWrapper wraps the first error encountered in a stream reader during conversion by StreamReaderWithConvert.
// The error returned by the convert function will not be wrapped.
// If the returned err is nil or is ErrNoValue, the stream chunk will be ignored
func WithErrWrapper(wrapper func(error) error) ConvertOption {
	return func(o *convertOptions) {
		o.ErrWrapper = wrapper
	}
}

// StreamReaderWithConvert converts the stream reader to another stream reader.
//
// eg.
//
//	intReader := StreamReaderFromArray([]int{1, 2, 3})
//	stringReader := StreamReaderWithConvert(sr, func(i int) (string, error) {
//		return fmt.Sprintf("val_%d", i), nil
//	})
//
//	defer stringReader.Close() // Close the reader if you using Recv(), or may cause memory/goroutine leak.
//	s, err := stringReader.Recv()
//	fmt.Println(s) // Output: val_1
func StreamReaderWithConvert[T, D any](sr *StreamReader[T], convert func(T) (D, error), opts ...ConvertOption) *StreamReader[D] {
	c := func(a any) (D, error) {
		return convert(a.(T))
	}

	return newStreamReaderWithConvert(sr, c, opts...)
}

func (srw *streamReaderWithConvert[T]) recv() (T, error) {
	for {
		out, err := srw.sr.recvAny()

		if err != nil {
			var t T
			if err == io.EOF {
				return t, err
			}
			if srw.errWrapper != nil {
				err = srw.errWrapper(err)
				if err != nil && !errors.Is(err, ErrNoValue) {
					return t, err
				}
			}

			return t, err
		}

		t, err := srw.convert(out)
		if err == nil {
			return t, nil
		}

		if !errors.Is(err, ErrNoValue) {
			return t, err
		}
	}
}

func (srw *streamReaderWithConvert[T]) close() {
	srw.sr.Close()
}

type reader[T any] interface {
	recv() (T, error)
	close()
}

func toStream[T any, Reader reader[T]](r Reader) *stream[T] {
	ret := newStream[T](5)

	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())

				var chunk T
				_ = ret.send(chunk, e)
			}

			ret.closeSend()
			r.close()
		}()

		for {
			out, err := r.recv()
			if err == io.EOF {
				break
			}

			closed := ret.send(out, err)
			if closed {
				break
			}
		}
	}()

	return ret
}

func (srw *streamReaderWithConvert[T]) toStream() *stream[T] {
	return toStream[T, *streamReaderWithConvert[T]](srw)
}

type cpStreamElement[T any] struct {
	once sync.Once
	next *cpStreamElement[T]
	item streamItem[T]
}

// copyStreamReaders creates multiple independent StreamReaders from a single StreamReader.
// Each child StreamReader can read from the original stream independently.
func copyStreamReaders[T any](sr *StreamReader[T], n int) []*StreamReader[T] {
	cpsr := &parentStreamReader[T]{
		sr:            sr,
		subStreamList: make([]*cpStreamElement[T], n),
		closedNum:     0,
	}

	// Initialize subStreamList with an empty element, which acts like a tail node.
	// A nil element (used for dereference) represents that the child has been closed.
	// It is challenging to link the previous and current elements when the length of the original channel is unknown.
	// Additionally, using a previous pointer complicates dereferencing elements, possibly requiring reference counting.
	elem := &cpStreamElement[T]{}

	for i := range cpsr.subStreamList {
		cpsr.subStreamList[i] = elem
	}

	ret := make([]*StreamReader[T], n)
	for i := range ret {
		ret[i] = &StreamReader[T]{
			csr: &childStreamReader[T]{
				parent: cpsr,
				index:  i,
			},
			typ: readerTypeChild,
		}
	}

	return ret
}

type parentStreamReader[T any] struct {
	// sr is the original StreamReader.
	sr *StreamReader[T]

	// subStreamList maps each child's index to its latest read chunk.
	// Each value comes from a hidden linked list of cpStreamElement.
	subStreamList []*cpStreamElement[T]

	// closedNum is the count of closed children.
	closedNum uint32
}

// peek is not safe for concurrent use with the same idx but is safe for different idx.
// Ensure that each child StreamReader uses a for-loop in a single goroutine.
func (p *parentStreamReader[T]) peek(idx int) (t T, err error) {
	elem := p.subStreamList[idx]
	if elem == nil {
		// Unexpected call to receive after the child has been closed.
		return t, ErrRecvAfterClosed
	}

	// The sync.Once here is used to:
	// 1. Write the content of this cpStreamElement.
	// 2. Initialize the 'next' field of this cpStreamElement with an empty cpStreamElement,
	//    similar to the initialization in copyStreamReaders.
	elem.once.Do(func() {
		t, err = p.sr.Recv()
		elem.item = streamItem[T]{chunk: t, err: err}
		if err != io.EOF {
			elem.next = &cpStreamElement[T]{}
			p.subStreamList[idx] = elem.next
		}
	})

	// The element has been set and will not be modified again.
	// Therefore, children can read this element's content and 'next' pointer concurrently.
	t = elem.item.chunk
	err = elem.item.err
	if err != io.EOF {
		p.subStreamList[idx] = elem.next
	}

	return t, err
}

func (p *parentStreamReader[T]) close(idx int) {
	if p.subStreamList[idx] == nil {
		return // avoid close multiple times
	}

	p.subStreamList[idx] = nil

	curClosedNum := atomic.AddUint32(&p.closedNum, 1)

	allClosed := int(curClosedNum) == len(p.subStreamList)
	if allClosed {
		p.sr.Close()
	}
}

type childStreamReader[T any] struct {
	parent *parentStreamReader[T]
	index  int
}

func (csr *childStreamReader[T]) recv() (T, error) {
	return csr.parent.peek(csr.index)
}

func (csr *childStreamReader[T]) toStream() *stream[T] {
	return toStream[T, *childStreamReader[T]](csr)
}

func (csr *childStreamReader[T]) close() {
	csr.parent.close(csr.index)
}

// MergeStreamReaders merge multiple StreamReader into one.
// it's useful when you want to merge multiple streams into one.
// e.g.
//
//	sr1, sr2 := schema.Pipe[string](2)
//	defer sr1.Close()
//	defer sr2.Close()
//
//	sr := schema.MergeStreamReaders([]*schema.StreamReader[string]{sr1, sr2})
//
//	defer sr.Close()
//	for chunk, err := sr.Recv() {
//		fmt.Println(chunk)
//	}
func MergeStreamReaders[T any](srs []*StreamReader[T]) *StreamReader[T] {
	if len(srs) < 1 {
		return nil
	}

	if len(srs) < 2 {
		return srs[0]
	}

	var arr []T
	var ss []*stream[T]

	for _, sr := range srs {
		switch sr.typ {
		case readerTypeStream:
			ss = append(ss, sr.st)
		case readerTypeArray:
			arr = append(arr, sr.ar.arr[sr.ar.index:]...)
		case readerTypeMultiStream:
			ss = append(ss, sr.msr.nonClosedStreams()...)
		case readerTypeWithConvert:
			ss = append(ss, sr.srw.toStream())
		case readerTypeChild:
			ss = append(ss, sr.csr.toStream())
		default:
			panic("impossible")
		}
	}

	if len(ss) == 0 {
		return &StreamReader[T]{
			typ: readerTypeArray,
			ar: &arrayReader[T]{
				arr:   arr,
				index: 0,
			},
		}
	}

	if len(arr) != 0 {
		s := arrToStream(arr)
		ss = append(ss, s)
	}

	return &StreamReader[T]{
		typ: readerTypeMultiStream,
		msr: newMultiStreamReader(ss),
	}
}

// MergeNamedStreamReaders merges multiple StreamReaders into one, preserving their names.
// When a source stream reaches EOF, the merged stream will return a SourceEOF error
// containing the name of the source stream that ended.
// This is useful when you need to track which source stream has completed.
// e.g.
//
//	sr1, sw1 := schema.Pipe[string](2)
//	sr2, sw2 := schema.Pipe[string](2)
//
//	namedStreams := map[string]*StreamReader[string]{
//		"stream1": sr1,
//		"stream2": sr2,
//	}
//
//	mergedSR := schema.MergeNamedStreamReaders(namedStreams)
//	defer mergedSR.Close()
//
//	for {
//		chunk, err := mergedSR.Recv()
//		if err != nil {
//			if sourceName, ok := schema.GetSourceName(err); ok {
//				fmt.Printf("Stream %s ended\n", sourceName)
//				continue
//			}
//			if errors.Is(err, io.EOF) {
//				break // All streams have ended
//			}
//			// Handle other errors
//		}
//		fmt.Println(chunk)
//	}
func MergeNamedStreamReaders[T any](srs map[string]*StreamReader[T]) *StreamReader[T] {
	if len(srs) < 1 {
		return nil
	}

	ss := make([]*StreamReader[T], len(srs))
	names := make([]string, len(srs))

	i := 0
	for name, sr := range srs {
		ss[i] = sr
		names[i] = name
		i++
	}

	return InternalMergeNamedStreamReaders(ss, names)
}

// InternalMergeNamedStreamReaders merges multiple readers with their names
// into a single multi-stream reader.
func InternalMergeNamedStreamReaders[T any](srs []*StreamReader[T], names []string) *StreamReader[T] {
	ss := make([]*stream[T], len(srs))

	for i, sr := range srs {
		ss[i] = sr.toStream()
	}

	msr := newMultiStreamReader(ss)
	msr.sourceReaderNames = names

	return &StreamReader[T]{
		typ: readerTypeMultiStream,
		msr: msr,
	}
}
