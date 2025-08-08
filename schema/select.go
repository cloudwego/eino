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

const maxSelectNum = 5

func receiveN[T any](chosenList []int, ss []*stream[T]) (int, *streamItem[T], bool) {
	return []func(chosenList []int, ss []*stream[T]) (index int, item *streamItem[T], ok bool){
		nil,
		func(chosenList []int, ss []*stream[T]) (int, *streamItem[T], bool) {
			item, ok := <-ss[chosenList[0]].items
			return chosenList[0], &item, ok
		},
		func(chosenList []int, ss []*stream[T]) (int, *streamItem[T], bool) {
			select {
			case item, ok := <-ss[chosenList[0]].items:
				return chosenList[0], &item, ok
			case item, ok := <-ss[chosenList[1]].items:
				return chosenList[1], &item, ok
			}
		},
		func(chosenList []int, ss []*stream[T]) (int, *streamItem[T], bool) {
			select {
			case item, ok := <-ss[chosenList[0]].items:
				return chosenList[0], &item, ok
			case item, ok := <-ss[chosenList[1]].items:
				return chosenList[1], &item, ok
			case item, ok := <-ss[chosenList[2]].items:
				return chosenList[2], &item, ok
			}
		},
		func(chosenList []int, ss []*stream[T]) (int, *streamItem[T], bool) {
			select {
			case item, ok := <-ss[chosenList[0]].items:
				return chosenList[0], &item, ok
			case item, ok := <-ss[chosenList[1]].items:
				return chosenList[1], &item, ok
			case item, ok := <-ss[chosenList[2]].items:
				return chosenList[2], &item, ok
			case item, ok := <-ss[chosenList[3]].items:
				return chosenList[3], &item, ok
			}
		},
		func(chosenList []int, ss []*stream[T]) (int, *streamItem[T], bool) {
			select {
			case item, ok := <-ss[chosenList[0]].items:
				return chosenList[0], &item, ok
			case item, ok := <-ss[chosenList[1]].items:
				return chosenList[1], &item, ok
			case item, ok := <-ss[chosenList[2]].items:
				return chosenList[2], &item, ok
			case item, ok := <-ss[chosenList[3]].items:
				return chosenList[3], &item, ok
			case item, ok := <-ss[chosenList[4]].items:
				return chosenList[4], &item, ok
			}
		},
	}[len(chosenList)](chosenList, ss)
}
