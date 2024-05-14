package dummyreceiver

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
)

type dummyReceiver struct {
	nextLogsConsumer consumer.Logs
}

func (r *dummyReceiver) Start(ctx context.Context, host component.Host) error {
	ld := plog.NewLogs()
	logRecord := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	logRecord.Body().SetStr("log body")
	r.nextLogsConsumer.ConsumeLogs(ctx, ld)
	return nil
}

func (r *dummyReceiver) Shutdown(ctx context.Context) error {
	return nil
}

func createLogsReceiver(_ context.Context, _ receiver.CreateSettings, _ component.Config, nextConsumer consumer.Logs) (receiver.Logs, error) {
	receiver := &dummyReceiver{
		nextLogsConsumer: nextConsumer,
	}
	return receiver, nil
}
