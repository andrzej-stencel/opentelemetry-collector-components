// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package elasticapmintakereceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elastic/opentelemetry-collector-components/internal/testutil"
	"github.com/elastic/opentelemetry-collector-components/receiver/elasticapmintakereceiver/internal/metadata"
	"github.com/elastic/opentelemetry-lib/agentcfg"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/golden"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/plogtest"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/pmetrictest"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/pdatatest/ptracetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

const testData = "testdata"

type fetcherMock struct {
	fetchFn func(context.Context, agentcfg.Query) (agentcfg.Result, error)
}

func (f *fetcherMock) Fetch(ctx context.Context, query agentcfg.Query) (agentcfg.Result, error) {
	return f.fetchFn(ctx, query)
}

func TestAgentCfgHandlerNoFetcher(t *testing.T) {
	testEndpoint := testutil.GetAvailableLocalAddress(t)
	rcvr, err := newElasticAPMIntakeReceiver(func(ctx context.Context, h component.Host) (agentcfg.Fetcher, error) {
		return nil, nil
	}, &Config{
		ServerConfig: confighttp.ServerConfig{
			Endpoint: testEndpoint,
		},
	}, receivertest.NewNopSettings(metadata.Type))
	require.NoError(t, err)

	ttCtx := context.Background()
	err = rcvr.Start(ttCtx, componenttest.NewNopHost())
	require.NoError(t, err)

	jsonQuery, err := json.Marshal(agentcfg.Query{})
	require.NoError(t, err)

	r, err := http.NewRequest("POST", "http://"+testEndpoint+agentConfigPath, bytes.NewBuffer(jsonQuery))
	require.NoError(t, err)

	r.Header.Add("Content-Type", "application/json")
	client := http.DefaultClient
	res, err := client.Do(r)
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, res.StatusCode)
	bodyBytes, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	assert.JSONEq(t, string([]byte(`{"error":"remote configuration fetcher not enabled"}`)), string(bodyBytes))
	require.NoError(t, res.Body.Close())

	err = rcvr.Shutdown(ttCtx)
	require.NoError(t, err)
}

func TestAgentCfgHandlerInvalidFetcher(t *testing.T) {
	tests := []struct {
		name  string
		query agentcfg.Query

		expectedStatusCode int
		expectedBody       []byte
	}{
		{
			name:  "empty request",
			query: agentcfg.Query{},

			expectedStatusCode: http.StatusServiceUnavailable,
			expectedBody:       []byte(`{"error":"no fetcher"}`),
		},
		{
			name: "service name request",
			query: agentcfg.Query{
				Service: agentcfg.Service{
					Name: "test-agent",
				},
			},

			expectedStatusCode: http.StatusServiceUnavailable,
			expectedBody:       []byte(`{"error":"no fetcher"}`),
		},
	}

	invalidFetcher := func(ctx context.Context, h component.Host) (agentcfg.Fetcher, error) {
		return nil, errors.New("no fetcher")
	}

	testEndpoint := testutil.GetAvailableLocalAddress(t)
	rcvr, err := newElasticAPMIntakeReceiver(invalidFetcher, &Config{
		ServerConfig: confighttp.ServerConfig{
			Endpoint: testEndpoint,
		},
	}, receivertest.NewNopSettings(metadata.Type))
	require.NoError(t, err)

	ttCtx := context.Background()
	err = rcvr.Start(ttCtx, componenttest.NewNopHost())
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonQuery, err := json.Marshal(tt.query)
			require.NoError(t, err)

			r, err := http.NewRequest("POST", "http://"+testEndpoint+agentConfigPath, bytes.NewBuffer(jsonQuery))
			require.NoError(t, err)

			r.Header.Add("Content-Type", "application/json")
			client := http.DefaultClient
			res, err := client.Do(r)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedStatusCode, res.StatusCode)
			bodyBytes, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			assert.JSONEq(t, string(tt.expectedBody), string(bodyBytes))
			require.NoError(t, res.Body.Close())
		})
	}

	err = rcvr.Shutdown(ttCtx)
	require.NoError(t, err)
}

func TestAgentCfgHandler(t *testing.T) {
	assertJsonBody := func(expectedBody, actualBody []byte) {
		assert.JSONEq(t, string(expectedBody), string(actualBody))
	}

	tests := []struct {
		name    string
		query   agentcfg.Query
		fetcher agentCfgFetcherFactory

		expectedStatusCode int
		expectedEtagHeader []string
		assertBodyFn       func([]byte)
	}{
		{
			name:  "empty request, service.name required",
			query: agentcfg.Query{},
			fetcher: func(ctx context.Context, h component.Host) (agentcfg.Fetcher, error) {
				return &fetcherMock{
					fetchFn: func(context.Context, agentcfg.Query) (agentcfg.Result, error) {
						return agentcfg.Result{}, nil
					},
				}, nil
			},

			expectedStatusCode: http.StatusBadRequest,
			assertBodyFn: func(expectedBody []byte) {
				assertJsonBody(expectedBody, []byte(`{"error":"service.name is required"}`))
			},
		},
		{
			name: "empty request, fetcher error",
			query: agentcfg.Query{
				Service: agentcfg.Service{
					Name: "test-agent",
				},
			},
			fetcher: func(ctx context.Context, h component.Host) (agentcfg.Fetcher, error) {
				return &fetcherMock{
					fetchFn: func(context.Context, agentcfg.Query) (agentcfg.Result, error) {
						return agentcfg.Result{}, errors.New("testing error")
					},
				}, nil
			},

			expectedStatusCode: http.StatusBadRequest,
			assertBodyFn: func(expectedBody []byte) {
				assertJsonBody(expectedBody, []byte(`{"error":"testing error"}`))
			},
		},
		{
			name: "not modified error",
			query: agentcfg.Query{
				Etag: "abc",
				Service: agentcfg.Service{
					Name: "test-agent",
				},
			},
			fetcher: func(ctx context.Context, h component.Host) (agentcfg.Fetcher, error) {
				return &fetcherMock{
					fetchFn: func(context.Context, agentcfg.Query) (agentcfg.Result, error) {
						return agentcfg.Result{
							Source: agentcfg.Source{
								Etag: "abc",
							},
						}, nil
					},
				}, nil
			},

			expectedStatusCode: http.StatusNotModified,
			expectedEtagHeader: []string{"\"abc\""},
			assertBodyFn: func(expectedBody []byte) {
				assert.Empty(t, expectedBody)
			},
		},
		{
			name: "new settings",
			query: agentcfg.Query{
				Etag: "abc",
				Service: agentcfg.Service{
					Name: "test-agent",
				},
			},
			fetcher: func(ctx context.Context, h component.Host) (agentcfg.Fetcher, error) {
				return &fetcherMock{
					fetchFn: func(context.Context, agentcfg.Query) (agentcfg.Result, error) {
						return agentcfg.Result{
							Source: agentcfg.Source{
								Settings: agentcfg.Settings{
									"transaction_max_spans": "123",
								},
								Etag: "cba",
							},
						}, nil
					},
				}, nil
			},

			expectedStatusCode: http.StatusOK,
			expectedEtagHeader: []string{"\"cba\""},
			assertBodyFn: func(expectedBody []byte) {
				assertJsonBody(expectedBody, []byte(`{"transaction_max_spans":"123"}`))
			},
		},
		{
			name: "new settings, no etag",
			query: agentcfg.Query{
				Service: agentcfg.Service{
					Name: "test-agent",
				},
			},
			fetcher: func(ctx context.Context, h component.Host) (agentcfg.Fetcher, error) {
				return &fetcherMock{
					fetchFn: func(context.Context, agentcfg.Query) (agentcfg.Result, error) {
						return agentcfg.Result{
							Source: agentcfg.Source{
								Settings: agentcfg.Settings{
									"transaction_max_spans": "123",
								},
								Etag: "cba",
							},
						}, nil
					},
				}, nil
			},

			expectedStatusCode: http.StatusOK,
			expectedEtagHeader: []string{"\"cba\""},
			assertBodyFn: func(expectedBody []byte) {
				assertJsonBody(expectedBody, []byte(`{"transaction_max_spans":"123"}`))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testEndpoint := testutil.GetAvailableLocalAddress(t)
			rcvr, err := newElasticAPMIntakeReceiver(tt.fetcher, &Config{
				ServerConfig: confighttp.ServerConfig{
					Endpoint: testEndpoint,
				},
				AgentConfig: AgentConfig{
					CacheDuration: 1 * time.Second,
				},
			}, receivertest.NewNopSettings(metadata.Type))
			require.NoError(t, err)

			ttCtx := context.Background()
			err = rcvr.Start(ttCtx, componenttest.NewNopHost())
			require.NoError(t, err)

			jsonQuery, err := json.Marshal(tt.query)
			require.NoError(t, err)

			r, err := http.NewRequest("POST", "http://"+testEndpoint+agentConfigPath, bytes.NewBuffer(jsonQuery))
			require.NoError(t, err)

			r.Header.Add("Content-Type", "application/json")
			client := http.DefaultClient
			res, err := client.Do(r)
			require.NoError(t, err)

			bodyBytes, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			tt.assertBodyFn(bodyBytes)
			require.NoError(t, res.Body.Close())

			assert.Equal(t, tt.expectedStatusCode, res.StatusCode)
			assert.Equal(t, tt.expectedEtagHeader, res.Header[Etag])

			err = rcvr.Shutdown(ttCtx)
			require.NoError(t, err)
		})
	}
}

func TestErrors(t *testing.T) {
	inputFiles_error := []struct {
		inputNdJsonFileName        string
		outputExpectedYamlFileName string
	}{
		{"errors.ndjson", "errors_expected.yaml"},
	}
	factory := NewFactory()
	testEndpoint := testutil.GetAvailableLocalAddress(t)
	cfg := &Config{
		ServerConfig: confighttp.ServerConfig{
			Endpoint: testEndpoint,
		},
	}

	set := receivertest.NewNopSettings(metadata.Type)
	nextLog := new(consumertest.LogsSink)
	receiver, _ := factory.CreateLogs(context.Background(), set, cfg, nextLog)

	if err := receiver.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		t.Errorf("Starting receiver failed: %v", err)
	}
	defer func() {
		if err := receiver.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	for _, tt := range inputFiles_error {
		t.Run(tt.inputNdJsonFileName, func(t *testing.T) {
			runComparisonForErrors(t, tt.inputNdJsonFileName, tt.outputExpectedYamlFileName, nextLog, testEndpoint)
		})
	}
}

func TestMetrics(t *testing.T) {
	var inputFiles_error = []struct {
		inputNdJsonFileName        string
		outputExpectedYamlFileName string
	}{
		{"metricsets.ndjson", "metricsets_expected.yaml"},
	}
	factory := NewFactory()
	testEndpoint := testutil.GetAvailableLocalAddress(t)
	cfg := &Config{
		ServerConfig: confighttp.ServerConfig{
			Endpoint: testEndpoint,
		},
	}

	set := receivertest.NewNopSettings(metadata.Type)
	nextMetrics := new(consumertest.MetricsSink)
	receiver, _ := factory.CreateMetrics(context.Background(), set, cfg, nextMetrics)

	if err := receiver.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		t.Errorf("Starting receiver failed: %v", err)
	}
	defer func() {
		if err := receiver.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	for _, tt := range inputFiles_error {
		t.Run(tt.inputNdJsonFileName, func(t *testing.T) {
			runComparisonForMetrics(t, tt.inputNdJsonFileName, tt.outputExpectedYamlFileName, nextMetrics, testEndpoint)
		})
	}
}

var inputFiles = []struct {
	inputNdJsonFileName        string
	outputExpectedYamlFileName string
}{
	{"invalid_ids.ndjson", "invalid_ids_expected.yaml"},
	{"transactions.ndjson", "transactions_expected.yaml"},
	{"spans.ndjson", "spans_expected.yaml"},
	{"unknown-span-type.ndjson", "unknown-span-type_expected.yaml"},
	{"transactions_spans.ndjson", "transactions_spans_expected.yaml"},
	{"language_name_mapping.ndjson", "language_name_mapping_expected.yaml"},
	{"span-links.ndjson", "span-links_expected.yaml"},
}

func TestTransactionsAndSpans(t *testing.T) {
	factory := NewFactory()
	testEndpoint := testutil.GetAvailableLocalAddress(t)
	cfg := &Config{
		ServerConfig: confighttp.ServerConfig{
			Endpoint: testEndpoint,
		},
	}

	set := receivertest.NewNopSettings(metadata.Type)
	nextTrace := new(consumertest.TracesSink)
	receiver, _ := factory.CreateTraces(context.Background(), set, cfg, nextTrace)

	if err := receiver.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		t.Errorf("Starting receiver failed: %v", err)
	}
	defer func() {
		if err := receiver.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown failed: %v", err)
		}
	}()

	for _, tt := range inputFiles {
		t.Run(tt.inputNdJsonFileName, func(t *testing.T) {
			runComparisonForTraces(t, tt.inputNdJsonFileName, tt.outputExpectedYamlFileName, nextTrace, testEndpoint)
		})
	}
}

func sendInput(t *testing.T, inputJsonFileName string, expectedYamlFileName string, testEndpoint string) {
	data, err := os.ReadFile(filepath.Join(testData, inputJsonFileName))
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	resp, err := http.Post("http://"+testEndpoint+intakeV2EventsPath, "application/x-ndjson", bytes.NewBuffer(data))
	if err != nil {
		t.Fatalf("failed to send HTTP request: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("unexpected status code: %v", resp.StatusCode)
	}
}

func runComparisonForTraces(t *testing.T, inputJsonFileName string, expectedYamlFileName string,
	nextTrace *consumertest.TracesSink, testEndpoint string,
) {
	nextTrace.Reset()

	sendInput(t, inputJsonFileName, expectedYamlFileName, testEndpoint)
	actualTraces := nextTrace.AllTraces()[0]
	expectedFile := filepath.Join(testData, expectedYamlFileName)
	// Use this line to generate the expected yaml file:
	// golden.WriteTraces(t, expectedFile, actualTraces)
	expectedTraces, err := golden.ReadTraces(expectedFile)
	require.NoError(t, err)
	require.NoError(t, ptracetest.CompareTraces(expectedTraces, actualTraces, ptracetest.IgnoreStartTimestamp(),
		ptracetest.IgnoreEndTimestamp()))
}

func runComparisonForErrors(t *testing.T, inputJsonFileName string, expectedYamlFileName string,
	nextLog *consumertest.LogsSink, testEndpoint string,
) {
	nextLog.Reset()

	sendInput(t, inputJsonFileName, expectedYamlFileName, testEndpoint)
	actualLogs := nextLog.AllLogs()[0]
	expectedFile := filepath.Join(testData, expectedYamlFileName)
	// Use this line to generate the expected yaml file:
	// golden.WriteLogs(t, expectedFile, actualLogs)
	expectedLogs, err := golden.ReadLogs(expectedFile)
	require.NoError(t, err)
	require.NoError(t, plogtest.CompareLogs(expectedLogs, actualLogs))
}

func runComparisonForMetrics(t *testing.T, inputJsonFileName string, expectedYamlFileName string,
	nextMetric *consumertest.MetricsSink, testEndpoint string,
) {

	nextMetric.Reset()
	sendInput(t, inputJsonFileName, expectedYamlFileName, testEndpoint)
	actualMetrics := nextMetric.AllMetrics()[0]
	expectedFile := filepath.Join(testData, expectedYamlFileName)
	// Use this line to generate the expected yaml file:
	// golden.WriteMetrics(t, expectedFile, actualMetrics)
	expectedMetrics, err := golden.ReadMetrics(expectedFile)
	require.NoError(t, err)
	require.NoError(t, pmetrictest.CompareMetrics(expectedMetrics, actualMetrics, pmetrictest.IgnoreMetricsOrder()))

}
