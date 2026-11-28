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

package taskcontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTimeoutControllerRequestAcknowledgement_BitsUT(t *testing.T) {
	ctx, controller := WithTimeoutController(context.Background())
	require.Same(t, controller, FromContext(ctx))
	require.Nil(t, FromContext(context.Background()))

	requestDone := make(chan error, 1)
	go func() {
		requestDone <- controller.RequestTimeout(context.Background(), "timed out")
	}()

	request := <-controller.Requests()
	require.Equal(t, "timed out", request.Reason)
	request.Complete(nil)
	controller.Close()
	require.NoError(t, <-requestDone)
}

func TestTimeoutControllerRejectsInvalidAndClosedRequests_BitsUT(t *testing.T) {
	_, controller := WithTimeoutController(context.Background())
	require.EqualError(
		t,
		controller.RequestTimeout(context.Background(), ""),
		"taskcontrol: timeout reason is required",
	)

	requestDone := make(chan error, 1)
	go func() {
		requestDone <- controller.RequestTimeout(context.Background(), "timed out")
	}()
	<-controller.Requests()
	controller.Close()
	controller.Close()
	require.ErrorIs(t, <-requestDone, ErrClosed)
	require.ErrorIs(
		t,
		controller.RequestTimeout(context.Background(), "timed out"),
		ErrClosed,
	)
}

func TestTimeoutControllerHonorsRequestContext_BitsUT(t *testing.T) {
	_, canceledController := WithTimeoutController(context.Background())
	canceledController.requests <- TimeoutRequest{result: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(
		t,
		canceledController.RequestTimeout(ctx, "timed out"),
		context.Canceled,
	)

	_, controller := WithTimeoutController(context.Background())
	wantErr := errors.New("rejected")
	requestDone := make(chan error, 1)
	go func() {
		requestDone <- controller.RequestTimeout(context.Background(), "timed out")
	}()
	request := <-controller.Requests()
	request.Complete(wantErr)
	require.ErrorIs(t, <-requestDone, wantErr)
}

func TestNilTimeoutControllerIsClosed(t *testing.T) {
	var controller *TimeoutController
	require.Nil(t, controller.Requests())
	require.Nil(t, controller.Done())
	controller.Close()
	require.ErrorIs(
		t,
		controller.RequestTimeout(context.Background(), "deadline"),
		ErrClosed,
	)
}
