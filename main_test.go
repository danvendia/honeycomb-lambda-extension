package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	libhoney "github.com/honeycombio/libhoney-go"
	"github.com/stretchr/testify/assert"
)

func Test_Configuration_BatchSendTimeout(t *testing.T) {
	testCases := []struct {
		desc            string
		timeoutEnvVar   string
		expectedTimeout time.Duration
	}{
		{
			desc:            "default",
			timeoutEnvVar:   "",
			expectedTimeout: defaultBatchSendTimeout,
		},
		{
			desc:            "set by user: duration seconds",
			timeoutEnvVar:   "9s",
			expectedTimeout: 9 * time.Second,
		},
		{
			desc:            "set by user: duration milliseconds",
			timeoutEnvVar:   "900ms",
			expectedTimeout: 900 * time.Millisecond,
		},
		{
			desc:            "set by user: integer",
			timeoutEnvVar:   "42",
			expectedTimeout: 42 * time.Second,
		},
		{
			desc:            "bad input: words",
			timeoutEnvVar:   "forty-two",
			expectedTimeout: defaultBatchSendTimeout,
		},
		{
			desc:            "bad input: unicode",
			timeoutEnvVar:   "🤷",
			expectedTimeout: defaultBatchSendTimeout,
		},
	}
	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			t.Setenv("HONEYCOMB_BATCH_SEND_TIMEOUT", tC.timeoutEnvVar)
			assert.Equal(t, tC.expectedTimeout, newTransmission().BatchSendTimeout)
		})
	}
}

func TestNewTransmissionHappyPathSend(t *testing.T) {
	testHandler := &TestHandler{
		sleep:        0,
		responseCode: 200,
		response:     []byte(`[{"status":200}]`),
	}
	testServer := httptest.NewServer(testHandler)
	defer testServer.Close()

	libhoneyClient, err := getTestLibhoneyClient(testServer.URL)
	assert.Nil(t, err, "unexpected error when creating client")

	err = sendTestEvent(libhoneyClient)
	assert.Nil(t, err, "unexpected error sending test event")

	txResponse := <-libhoneyClient.TxResponses()
	assert.Nil(t, txResponse.Err, "unexpected error in response")
	assert.Equal(t, 1, testHandler.callCount, "expected a single client request")
	assert.Equal(t, 200, txResponse.StatusCode)
}

func TestNewTransmissionBatchSendTimeout(t *testing.T) {
	t.Setenv("HONEYCOMB_BATCH_SEND_TIMEOUT", "10ms")
	testHandler := &TestHandler{
		sleep:        time.Second,
		responseCode: 200,
		response:     []byte(`[{"status":200}]`),
	}
	testServer := httptest.NewServer(testHandler)
	defer testServer.Close()

	libhoneyClient, err := getTestLibhoneyClient(testServer.URL)
	assert.Nil(t, err, "unexpected error when creating client")

	err = sendTestEvent(libhoneyClient)
	assert.Nil(t, err, "unexpected error sending test event")

	txResponse := <-libhoneyClient.TxResponses()
	assert.Equal(t, 2, testHandler.callCount, "expected 2 requests due to retry")
	assert.NotNil(t, txResponse.Err, "expected error in response")
	txResponseErr, ok := txResponse.Err.(net.Error)
	assert.True(t, ok, "expected a net.Error but got %v", txResponseErr)
	assert.True(t, txResponseErr.Timeout(), "expected error to be a timeout")
}

func TestNewTransmissionConnectTimeout(t *testing.T) {
	t.Setenv("HONEYCOMB_TRANSPORT_DIALER_TIMEOUT", "10ms")

	testHandler := &TestHandler{}
	testServer := httptest.NewServer(testHandler)
	testServer.Close()

	libhoneyClient, err := getTestLibhoneyClient(testServer.URL)
	assert.Nil(t, err, "unexpected error creating client")

	err = sendTestEvent(libhoneyClient)
	assert.Nil(t, err, "unexpected error sending test event")

	txResponse := <-libhoneyClient.TxResponses()
	assert.Equal(t, 0, testHandler.callCount, "expected 0 requests as server was shutdown")
	assert.NotNil(t, txResponse.Err, "expected response to be an error")
	txResponseErr, ok := txResponse.Err.(net.Error)
	assert.True(t, ok, fmt.Sprintf("expected a net.Error but got %v", txResponseErr))
	assert.ErrorIs(t, txResponseErr, syscall.ECONNREFUSED,
		fmt.Sprintf("expected connection refused error but got %v", txResponseErr))
}

// ###########################################
// Test implementations
// ###########################################

// getTestLibhoneyClient creates a libhoney client that points at specified URL
// and uses newTransmission to construct Transmission
func getTestLibhoneyClient(url string) (*libhoney.Client, error) {
	return libhoney.NewClient(libhoney.ClientConfig{
		APIKey:       "test",
		Dataset:      "test",
		APIHost:      url,
		Transmission: newTransmission(),
	})
}

// sendTestEvent creates a test event and flushes it
func sendTestEvent(libhoneyClient *libhoney.Client) error {
	ev := libhoneyClient.NewEvent()
	ev.Add(map[string]interface{}{
		"duration_ms": 153.12,
		"method":      "test",
	})

	err := ev.Send()
	if err != nil {
		return err
	}

	libhoneyClient.Flush()
	return nil
}

// TestHandler is a handler used for mocking server responses for the underlying HTTP calls
// made by libhoney-go to test various scenarios
type TestHandler struct {
	sync.Mutex
	callCount    int
	sleep        time.Duration
	responseCode int
	response     []byte
}

func (h *TestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Lock()
	h.callCount++
	h.Unlock()

	_, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "can't read body", http.StatusBadRequest)
		return
	}

	select {
	case <-time.After(h.sleep):
		w.WriteHeader(h.responseCode)
		w.Write(h.response)
		return
	}
}
