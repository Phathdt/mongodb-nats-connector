package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"io"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ThreeDotsLabs/watermill-jetstream/pkg/jetstream"
	"github.com/damianiandrea/mongodb-nats-connector/internal/server"
)

const (
	defaultName = "nats"
)

var (
	ErrClientDisconnected = errors.New("could not reach nats: connection closed")
)

type Client interface {
	server.NamedMonitor
	io.Closer

	AddStream(ctx context.Context, opts *AddStreamOptions) error
	Publish(ctx context.Context, opts *PublishOptions) error
}

type AddStreamOptions struct {
	StreamName string
}

type PublishOptions struct {
	Subj  string
	MsgId string
	Data  []byte
}

var _ Client = &DefaultClient{}

type DefaultClient struct {
	url    string
	name   string
	logger *slog.Logger

	conn      *nats.Conn
	js        nats.JetStreamContext
	publisher *jetstream.Publisher
}

func NewDefaultClient(opts ...ClientOption) (*DefaultClient, error) {
	c := &DefaultClient{
		name:   defaultName,
		logger: slog.Default(),
	}

	for _, opt := range opts {
		opt(c)
	}

	conn, err := nats.Connect(c.url,
		nats.DisconnectErrHandler(func(conn *nats.Conn, err error) {
			c.logger.Error("disconnected from nats", "err", err)
		}),
		nats.ReconnectHandler(func(conn *nats.Conn) {
			c.logger.Info("reconnected to nats", "url", conn.ConnectedUrlRedacted())
		}),
		nats.ClosedHandler(func(conn *nats.Conn) {
			c.logger.Info("nats connection closed")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("could not connect to nats: %v", err)
	}
	c.conn = conn

	js, _ := conn.JetStream()
	c.js = js

	options := []nats.Option{
		nats.RetryOnFailedConnect(true),
		nats.Timeout(30 * time.Second),
		nats.ReconnectWait(1 * time.Second),
	}

	marshaler := &jetstream.GobMarshaler{}
	logger := watermill.NewStdLogger(false, false)
	c.publisher, err = jetstream.NewPublisher(
		jetstream.PublisherConfig{
			URL:         c.url,
			NatsOptions: options,
			Marshaler:   marshaler,
		},
		logger,
	)

	c.logger.Info("connected to nats", "url", conn.ConnectedUrlRedacted())
	return c, nil
}

func (c *DefaultClient) Name() string {
	return c.name
}

func (c *DefaultClient) Monitor(_ context.Context) error {
	if closed := c.conn.IsClosed(); closed {
		return ErrClientDisconnected
	}
	return nil
}

func (c *DefaultClient) Close() error {
	c.conn.Close()
	return nil
}

func (c *DefaultClient) AddStream(_ context.Context, opts *AddStreamOptions) error {
	_, err := c.js.AddStream(&nats.StreamConfig{
		Name:     opts.StreamName,
		Subjects: []string{fmt.Sprintf("%s.*", opts.StreamName)},
		Storage:  nats.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("could not add nats stream %v: %v", opts.StreamName, err)
	}
	c.logger.Debug("added nats stream", "streamName", opts.StreamName)
	return nil
}

func (c *DefaultClient) Publish(_ context.Context, opts *PublishOptions) error {
	payload, err := json.Marshal(opts.Data)
	if err != nil {
		return err
	}

	fmt.Printf("payload = %+v\n", payload)
	msg := message.NewMessage(watermill.NewUUID(), payload)
	if err = c.publisher.Publish(opts.Subj, msg); err != nil {
		return err
	}

	c.logger.Debug("published message", "subj", opts.Subj, "data", string(opts.Data))

	return nil
}

type ClientOption func(*DefaultClient)

func WithNatsUrl(url string) ClientOption {
	return func(c *DefaultClient) {
		if url != "" {
			c.url = url
		}
	}
}

func WithLogger(logger *slog.Logger) ClientOption {
	return func(c *DefaultClient) {
		if logger != nil {
			c.logger = logger
		}
	}
}
