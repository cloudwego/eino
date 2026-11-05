/*
 * Copyright 2026 CloudWeGo Authors
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

package toolsearch

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func rankedToolNames(infos []*schema.ToolInfo) []string {
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	return names
}

func TestSearchFuncArgumentsAndResults(t *testing.T) {
	tools := makeToolMap(ti("first", ""), ti("second", ""), ti("third", ""))
	for _, tc := range []struct {
		name      string
		limit     *int
		wantLimit int
		want      []string
	}{
		{"default", nil, 5, []string{"third", "first", "second"}},
		{"zero", intPtr(0), 5, []string{"third", "first", "second"}},
		{"negative", intPtr(-2), 5, []string{"third", "first", "second"}},
		{"one", intPtr(1), 1, []string{"third"}},
		{"two", intPtr(2), 2, []string{"third", "first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "value")
			called := 0
			got, err := search(ctx, searchJSON("  +custom query \n", tc.limit), tools,
				func(gotCtx context.Context, query string, limit int) ([]string, error) {
					called++
					require.Same(t, ctx, gotCtx)
					require.Equal(t, "value", gotCtx.Value(contextKey{}))
					require.Equal(t, "+custom query", query)
					require.Equal(t, tc.wantLimit, limit)
					return []string{"unknown", "", "third", "third", "first", "second"}, nil
				})
			require.NoError(t, err)
			require.Equal(t, 1, called)
			require.Equal(t, tc.want, rankedToolNames(got))
			for _, info := range got {
				require.Same(t, tools[info.Name], info)
			}
		})
	}
}

func TestSearchFuncValidationAndSelect(t *testing.T) {
	tools := makeToolMap(ti("first", ""), ti("second", ""))
	neverCall := func(context.Context, string, int) ([]string, error) {
		t.Fatal("search function must not be called")
		return nil, nil
	}
	for _, input := range []string{`{`, `{}`, `{"query":"  "}`, `{"query":3}`, `{"query":"first","max_results":"one"}`} {
		_, err := search(context.Background(), input, tools, neverCall)
		require.Error(t, err)
	}
	for _, query := range []string{"select:second,unknown,first,second", " select:second, unknown, first, second  "} {
		got, err := search(context.Background(), searchJSON(query, intPtr(1)), tools, neverCall)
		require.NoError(t, err)
		require.Equal(t, []string{"second", "first", "second"}, rankedToolNames(got))
	}
	got, err := search(context.Background(), searchJSON("select:", nil), tools, neverCall)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSearchFuncErrorsAndCancellation(t *testing.T) {
	tools := makeToolMap(ti("first", "first"))
	input := searchJSON("first", nil)
	sentinel := errors.New("index unavailable")
	t.Run("error without fallback", func(t *testing.T) {
		got, err := search(context.Background(), input, tools, func(context.Context, string, int) ([]string, error) {
			return []string{"first"}, sentinel
		})
		require.ErrorIs(t, err, sentinel)
		require.Nil(t, got)
	})
	t.Run("empty without fallback", func(t *testing.T) {
		got, err := search(context.Background(), input, tools, func(context.Context, string, int) ([]string, error) {
			return nil, nil
		})
		require.NoError(t, err)
		require.Empty(t, got)
	})
	t.Run("pre-canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := search(ctx, input, tools, func(context.Context, string, int) ([]string, error) {
			t.Fatal("called after cancellation")
			return nil, nil
		})
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		defer cancel()
		_, err := search(ctx, input, tools, func(context.Context, string, int) ([]string, error) {
			t.Fatal("called after deadline")
			return nil, nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("canceled during callback", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		got, err := search(ctx, input, tools, func(ctx context.Context, _ string, _ int) ([]string, error) {
			cancel()
			<-ctx.Done()
			return []string{"first"}, nil
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, got)
	})
}

func TestSearchFuncFilteringRandomized(t *testing.T) {
	// Compare filtering and limiting against an independent linear reference.
	rng := rand.New(rand.NewSource(862))
	tools := makeToolMap(ti("a", ""), ti("b", ""), ti("c", ""))
	candidates := []string{"a", "b", "c", "unknown", "", "A"}
	for trial := 0; trial < 1000; trial++ {
		names := make([]string, rng.Intn(30))
		for i := range names {
			names[i] = candidates[rng.Intn(len(candidates))]
		}
		limit := 1 + rng.Intn(5)
		want := make([]string, 0)
		for _, name := range names {
			if _, ok := tools[name]; !ok {
				continue
			}
			duplicate := false
			for _, seen := range want {
				if name == seen {
					duplicate = true
				}
			}
			if !duplicate && len(want) < limit {
				want = append(want, name)
			}
		}
		got, err := search(context.Background(), searchJSON("query", &limit), tools, func(context.Context, string, int) ([]string, error) { return names, nil })
		require.NoError(t, err)
		require.Equal(t, want, rankedToolNames(got), "trial %d", trial)
	}
}

func TestSearchFuncDefaultEquivalence(t *testing.T) {
	tools := makeToolMap(ti("read_file", "Read file"), ti("write_file", "Write file"), ti("search", "Search file"))
	for _, query := range []string{"file", "+read file", "SEARCH", "unmatched", "  write  "} {
		for limit := 1; limit < 5; limit++ {
			t.Run(fmt.Sprintf("%s/%d", query, limit), func(t *testing.T) {
				got, err := search(context.Background(), searchJSON(query, &limit), tools, nil)
				require.NoError(t, err)
				require.Equal(t, keywordSearch(query, limit, tools), rankedToolNames(got))
			})
		}
	}
}

func TestSearchFuncToolDescriptions(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, custom := range []bool{false, true} {
			var fn SearchFunc
			if custom {
				fn = func(context.Context, string, int) ([]string, error) { return nil, nil }
			}
			info, err := newToolSearchTool(nil, native, fn).Info(context.Background())
			require.NoError(t, err)
			if custom {
				require.Contains(t, info.Desc, "configured search strategy")
				require.NotContains(t, info.Desc, "+slack")
			} else {
				require.Equal(t, toolDescription, info.Desc)
			}
		}
	}
}

func TestSearchFuncConcurrentCalls(t *testing.T) {
	tools := makeToolMap(ti("first", ""), ti("second", ""))
	fn := func(ctx context.Context, query string, _ int) ([]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return []string{query, query, "unknown"}, nil
	}
	for _, native := range []bool{false, true} {
		searchTool := newToolSearchTool(tools, native, fn)
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			name := []string{"first", "second"}[i%2]
			wg.Add(1)
			go func() {
				defer wg.Done()
				if native {
					out, err := searchTool.(*modelToolSearchTool).InvokableRun(context.Background(), &schema.ToolArgument{Text: searchJSON(name, nil)})
					if !assert.NoError(t, err) {
						return
					}
					assert.Equal(t, []string{name}, rankedToolNames(out.Parts[0].ToolSearchResult.Tools))
				} else {
					out, err := searchTool.(*toolSearchTool).InvokableRun(context.Background(), searchJSON(name, nil))
					if !assert.NoError(t, err) {
						return
					}
					assert.JSONEq(t, fmt.Sprintf(`{"matches":["%s"]}`, name), out)
				}
			}()
		}
		wg.Wait()
	}
}
