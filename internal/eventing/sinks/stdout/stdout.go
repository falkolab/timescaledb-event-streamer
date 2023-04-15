package stdout

import (
	"encoding/json"
	"github.com/noctarius/timescaledb-event-streamer/internal/supporting/logging"
	spiconfig "github.com/noctarius/timescaledb-event-streamer/spi/config"
	"github.com/noctarius/timescaledb-event-streamer/spi/schema"
	"github.com/noctarius/timescaledb-event-streamer/spi/sink"
	"time"
)

func init() {
	sink.RegisterSink(spiconfig.Stdout, newStdoutSink)
}

type stdoutSink struct {
	logger *logging.Logger
}

func newStdoutSink(_ *spiconfig.Config) (sink.Sink, error) {
	return &stdoutSink{
		logger: logging.NewLogger("StdoutSink"),
	}, nil
}

func (s *stdoutSink) Emit(_ time.Time, topicName string, _, envelope schema.Struct) error {
	delete(envelope, "schema")
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	s.logger.Infof("===> /%s: \t%s\n", topicName, string(data))
	return nil
}
