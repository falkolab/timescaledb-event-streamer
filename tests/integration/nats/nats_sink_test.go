/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements. See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-errors/errors"
	"github.com/nats-io/nats.go"
	"github.com/noctarius/timescaledb-event-streamer/internal/logging"
	"github.com/noctarius/timescaledb-event-streamer/internal/sysconfig"
	"github.com/noctarius/timescaledb-event-streamer/internal/waiting"
	spiconfig "github.com/noctarius/timescaledb-event-streamer/spi/config"
	"github.com/noctarius/timescaledb-event-streamer/testsupport"
	"github.com/noctarius/timescaledb-event-streamer/testsupport/containers"
	"github.com/noctarius/timescaledb-event-streamer/testsupport/testrunner"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"testing"
	"time"
)

type NatsIntegrationTestSuite struct {
	testrunner.TestRunner
}

func TestNatsIntegrationTestSuite(
	t *testing.T,
) {

	suite.Run(t, new(NatsIntegrationTestSuite))
}

func (nits *NatsIntegrationTestSuite) Test_Nats_Sink() {
	topicPrefix := lo.RandomString(10, lo.LowerCaseLettersCharset)

	natsLogger, err := logging.NewLogger("Test_Nats_Sink")
	if err != nil {
		nits.T().Error(err)
	}

	var natsUrl string
	var natsContainer testcontainers.Container

	nits.RunTest(
		func(ctx testrunner.Context) error {
			// Collect logs
			natsContainer.FollowOutput(&testrunner.ContainerLogForwarder{Logger: natsLogger})
			natsContainer.StartLogProducer(context.Background())

			conn, err := nats.Connect(natsUrl, nats.DontRandomize(), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
			if err != nil {
				return err
			}

			js, err := conn.JetStream(nats.PublishAsyncMaxPending(256))
			if err != nil {
				return err
			}

			subjectName := fmt.Sprintf(
				"%s.%s.%s", topicPrefix,
				testrunner.GetAttribute[string](ctx, "schemaName"),
				testrunner.GetAttribute[string](ctx, "tableName"),
			)

			streamName := lo.RandomString(10, lo.LowerCaseLettersCharset)
			groupName := lo.RandomString(10, lo.LowerCaseLettersCharset)

			natsLogger.Println("Creating NATS JetStream stream...")
			_, err = js.AddStream(&nats.StreamConfig{
				Name:     streamName,
				Subjects: []string{subjectName},
			})
			if err != nil {
				return err
			}

			waiter := waiting.NewWaiterWithTimeout(time.Minute)
			envelopes := make([]testsupport.Envelope, 0)
			_, err = js.QueueSubscribe(subjectName, groupName, func(msg *nats.Msg) {
				envelope := testsupport.Envelope{}
				if err := json.Unmarshal(msg.Data, &envelope); err != nil {
					msg.Nak()
					nits.T().Error(err)
				}
				natsLogger.Debugf("EVENT: %+v", envelope)
				envelopes = append(envelopes, envelope)
				if len(envelopes) >= 10 {
					waiter.Signal()
				}
				msg.Ack()
			}, nats.ManualAck())
			if err != nil {
				return err
			}

			if _, err := ctx.Exec(context.Background(),
				fmt.Sprintf(
					"INSERT INTO \"%s\" SELECT ts, ROW_NUMBER() OVER (ORDER BY ts) AS val FROM GENERATE_SERIES('2023-03-25 00:00:00'::TIMESTAMPTZ, '2023-03-25 00:09:59'::TIMESTAMPTZ, INTERVAL '1 minute') t(ts)",
					testrunner.GetAttribute[string](ctx, "tableName"),
				),
			); err != nil {
				return err
			}

			if err := waiter.Await(); err != nil {
				return err
			}

			for i, envelope := range envelopes {
				assert.Equal(nits.T(), i+1, int(envelope.Payload.After["val"].(float64)))
			}
			return nil
		},

		testrunner.WithSetup(func(setupContext testrunner.SetupContext) error {
			sn, tn, err := setupContext.CreateHypertable("ts", time.Hour*24,
				testsupport.NewColumn("ts", "timestamptz", false, false, nil),
				testsupport.NewColumn("val", "integer", false, false, nil),
			)
			if err != nil {
				return err
			}
			testrunner.Attribute(setupContext, "schemaName", sn)
			testrunner.Attribute(setupContext, "tableName", tn)

			nC, nU, err := containers.SetupNatsContainer()
			if err != nil {
				return errors.Wrap(err, 0)
			}
			natsUrl = nU
			natsContainer = nC

			setupContext.AddSystemConfigConfigurator(func(config *sysconfig.SystemConfig) {
				config.Topic.Prefix = topicPrefix
				config.Sink.Type = spiconfig.NATS
				config.Sink.Nats = spiconfig.NatsConfig{
					Address:       natsUrl,
					Authorization: spiconfig.UserInfo,
					UserInfo: spiconfig.NatsUserInfoConfig{
						Username: "",
						Password: "",
					},
				}
			})

			return nil
		}),

		testrunner.WithTearDown(func(ctx testrunner.Context) error {
			if natsContainer != nil {
				natsContainer.Terminate(context.Background())
			}
			return nil
		}),
	)
}
