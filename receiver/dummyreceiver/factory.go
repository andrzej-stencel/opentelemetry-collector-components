package dummyreceiver

import (
	"github.com/andrzej-stencel/opentelemetry-collector-components/receiver/dummyreceiver/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/receiver"
)

func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		func() component.Config { return &struct{}{} },
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability),
	)
}
